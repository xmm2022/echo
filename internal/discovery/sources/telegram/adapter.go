package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/gotd/td/session"
	gotdtelegram "github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/xmm2022/echo/internal/config"
	"github.com/xmm2022/echo/internal/discovery"
	"github.com/xmm2022/echo/internal/secrets"
)

const defaultHistoryLimit = 100

type Message struct {
	ID   int64
	Date int64
	Text string
}

type Channel struct {
	Ref        string
	SessionRef string
	Cursor     Cursor
}

type Client interface {
	History(ctx context.Context, sessionRef string, channelRef string, afterMessageID int64, limit int) ([]Message, error)
}

type Adapter struct {
	client Client
}

type Resource struct {
	Provider    string
	ExternalKey string
	Title       string
	ShareCode   string
	ReceiveCode string
	RawText     string
	MessageID   int64
	MessageDate int64
}

type crawlConfig struct {
	Channels []crawlChannel `json:"channels"`
}

type crawlChannel struct {
	Ref             string `json:"ref"`
	SessionRef      string `json:"session_ref"`
	LastMessageID   int64  `json:"last_message_id"`
	LastMessageDate int64  `json:"last_message_date"`
}

type MTProtoClient struct {
	sessionRoot string
	apiID       int
	apiHash     string
}

type errorClient struct {
	err error
}

var (
	shareURLPattern    = regexp.MustCompile(`https://115\.com/s/[^\s<>"']+`)
	querySecretPattern = regexp.MustCompile(`(?i)([?&])(?:password|pwd|receive_code|receiveCode)=[^\s&#]+`)
	textSecretPattern  = regexp.MustCompile(`(?i)\b(?:password|pwd|receive_code|receiveCode)\s*=\s*[^\s&]+`)

	errResolveSessionRef = discovery.AuthFailedError{Message: "telegram history: resolve session ref failed"}
	errResolveAPIHash    = discovery.AuthFailedError{Message: "telegram config: resolve api hash failed"}
)

func NewAdapter(client Client) *Adapter {
	return &Adapter{client: client}
}

func (a *Adapter) CrawlChannel(ctx context.Context, ch Channel) ([]Resource, Cursor, error) {
	messages, err := a.client.History(ctx, ch.SessionRef, ch.Ref, ch.Cursor.LastMessageID, defaultHistoryLimit)
	if err != nil {
		return nil, ch.Cursor, err
	}

	next := ch.Cursor
	var out []Resource
	for _, message := range messages {
		if message.ID <= ch.Cursor.LastMessageID {
			continue
		}
		if message.ID > next.LastMessageID {
			next.LastMessageID = message.ID
		}
		if message.Date > next.LastMessageDate {
			next.LastMessageDate = message.Date
		}
		out = append(out, parseMessage(ch.Ref, message)...)
	}
	return out, next, nil
}

func (a *Adapter) Crawl(ctx context.Context, source discovery.Source) (discovery.SourceCrawlResult, error) {
	var cfg crawlConfig
	if err := json.Unmarshal([]byte(source.ConfigText()), &cfg); err != nil {
		return discovery.SourceCrawlResult{}, fmt.Errorf("telegram crawl: parse source config: %w", err)
	}

	out := discovery.SourceCrawlResult{}
	for _, configured := range cfg.Channels {
		resources, next, err := a.CrawlChannel(ctx, Channel{
			Ref:        configured.Ref,
			SessionRef: configured.SessionRef,
			Cursor: Cursor{
				LastMessageID:   configured.LastMessageID,
				LastMessageDate: configured.LastMessageDate,
			},
		})
		if err != nil {
			return discovery.SourceCrawlResult{}, fmt.Errorf("telegram crawl %s: %w", configured.Ref, err)
		}
		for _, resource := range resources {
			out.Items = append(out.Items, toParsedResource(resource, configured.Ref))
		}
		out.TelegramCursors = append(out.TelegramCursors, discovery.TelegramCursorUpdate{
			ChannelRef:      configured.Ref,
			LastMessageID:   next.LastMessageID,
			LastMessageDate: next.LastMessageDate,
		})
	}
	return out, nil
}

func NewMTProtoClient(sessionRoot string, apiID int, apiHash string) *MTProtoClient {
	return &MTProtoClient{
		sessionRoot: sessionRoot,
		apiID:       apiID,
		apiHash:     apiHash,
	}
}

func NewMTProtoClientFromConfig(cfg config.TelegramConfig, secretsRoot string, logger *slog.Logger) Client {
	apiHash, err := resolveAPIHash(cfg.APIHashRef, secretsRoot)
	if err != nil {
		if logger != nil {
			logger.Error(errResolveAPIHash.Error(), "kind", "api_hash_ref")
		}
		return errorClient{err: errResolveAPIHash}
	}
	return NewMTProtoClient(cfg.SessionRoot, cfg.APIID, apiHash)
}

func (c *MTProtoClient) History(ctx context.Context, sessionRef string, channelRef string, afterMessageID int64, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = defaultHistoryLimit
	}
	sessionPath, err := secrets.Resolve(c.sessionRoot, sessionRef)
	if err != nil {
		return nil, errResolveSessionRef
	}

	client := gotdtelegram.NewClient(c.apiID, c.apiHash, gotdtelegram.Options{
		NoUpdates:      true,
		SessionStorage: &session.FileStorage{Path: sessionPath},
	})

	var out []Message
	err = client.Run(ctx, func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return convertFloodWait(err)
		}
		if !status.Authorized {
			return discovery.AuthFailedError{Message: "telegram history: session is not authorized"}
		}

		api := client.API()
		peer, err := resolveInputPeer(ctx, api, channelRef)
		if err != nil {
			return convertFloodWait(err)
		}
		history, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:  peer,
			MinID: int(afterMessageID),
			Limit: limit,
		})
		if err != nil {
			return convertFloodWait(err)
		}
		out = messagesFromHistory(history)
		return nil
	})
	if err != nil {
		return nil, convertFloodWait(err)
	}
	return out, nil
}

func (c errorClient) History(ctx context.Context, sessionRef string, channelRef string, afterMessageID int64, limit int) ([]Message, error) {
	return nil, c.err
}

func parseMessage(channelRef string, message Message) []Resource {
	matches := shareURLPattern.FindAllString(message.Text, -1)
	out := make([]Resource, 0, len(matches))
	for linkIndex, match := range matches {
		link, err := parseShareURL(match)
		if err != nil {
			continue
		}
		shareCode := shareCodeFromURL(link)
		receiveCode := receiveCodeFromURL(link)
		out = append(out, Resource{
			Provider:    "115",
			ExternalKey: fmt.Sprintf("tg:%s:%d:115:%d", channelRef, message.ID, linkIndex),
			Title:       redactedTitle(message.Text, message.ID, shareCode, receiveCode),
			ShareCode:   shareCode,
			ReceiveCode: receiveCode,
			RawText:     message.Text,
			MessageID:   message.ID,
			MessageDate: message.Date,
		})
	}
	return out
}

func parseShareURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimRight(raw, ".,;)"))
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "115.com") {
		return nil, fmt.Errorf("not a 115 share URL")
	}
	if shareCodeFromURL(parsed) == "" {
		return nil, fmt.Errorf("not a 115 share path")
	}
	return parsed, nil
}

func shareCodeFromURL(link *url.URL) string {
	parts := strings.Split(strings.Trim(link.EscapedPath(), "/"), "/")
	if len(parts) < 2 || parts[0] != "s" {
		return ""
	}
	code, err := url.PathUnescape(parts[1])
	if err != nil {
		return parts[1]
	}
	return code
}

func receiveCodeFromURL(link *url.URL) string {
	values := link.Query()
	for _, key := range []string{"password", "pwd", "receive_code", "receiveCode"} {
		if value := values.Get(key); value != "" {
			return value
		}
	}
	return ""
}

func redactedTitle(text string, messageID int64, shareCode string, receiveCode string) string {
	redacted := redactText(text, shareCode, receiveCode)
	redacted = shareURLPattern.ReplaceAllString(redacted, "[115 share link]")
	redacted = strings.Join(strings.Fields(redacted), " ")
	if redacted == "" || redacted == "[115 share link]" {
		return fmt.Sprintf("Telegram message %d", messageID)
	}
	return redacted
}

func redactText(text, shareCode string, receiveCode string) string {
	redacted := querySecretPattern.ReplaceAllString(text, "$1[REDACTED]")
	redacted = textSecretPattern.ReplaceAllString(redacted, "[REDACTED]")
	if shareCode != "" {
		redacted = strings.ReplaceAll(redacted, shareCode, "[REDACTED]")
	}
	if receiveCode != "" {
		redacted = strings.ReplaceAll(redacted, receiveCode, "[REDACTED]")
	}
	return redacted
}

func toParsedResource(resource Resource, channelRef string) discovery.ParsedResource {
	title := redactedTitle(resource.RawText, resource.MessageID, resource.ShareCode, resource.ReceiveCode)
	parsedJSON, _ := json.Marshal(map[string]any{
		"provider":              resource.Provider,
		"link_kind":             string(discovery.Link115Share),
		"source":                "telegram",
		"telegram_channel_ref":  channelRef,
		"telegram_message_id":   resource.MessageID,
		"telegram_message_date": resource.MessageDate,
		"title_redacted":        title,
	})
	return discovery.ParsedResource{
		Provider:        discovery.Provider115,
		LinkKind:        discovery.Link115Share,
		ExternalKey:     resource.ExternalKey,
		Title:           title,
		ShareCode:       resource.ShareCode,
		ReceiveCode:     resource.ReceiveCode,
		RawText:         []byte(resource.RawText),
		RawTextRedacted: redactText(resource.RawText, resource.ShareCode, resource.ReceiveCode),
		FeatureJSON:     "{}",
		ParsedJSON:      string(parsedJSON),
		ObservedAt:      time.Now(),
	}
}

func resolveAPIHash(ref string, secretsRoot string) (string, error) {
	if strings.HasPrefix(ref, "env:") {
		hash, err := secrets.ResolveEnv(ref)
		if err != nil {
			return "", errResolveAPIHash
		}
		hash = strings.TrimSpace(hash)
		if hash == "" {
			return "", errResolveAPIHash
		}
		return hash, nil
	}
	path, err := secrets.Resolve(secretsRoot, ref)
	if err != nil {
		return "", errResolveAPIHash
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", errResolveAPIHash
	}
	hash := strings.TrimSpace(string(data))
	if hash == "" {
		return "", errResolveAPIHash
	}
	return hash, nil
}

func resolveInputPeer(ctx context.Context, api *tg.Client, channelRef string) (tg.InputPeerClass, error) {
	username := normalizeChannelRef(channelRef)
	if username == "" {
		return nil, errors.New("telegram channel ref is empty")
	}
	resolved, err := api.ContactsResolveUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	return inputPeerFromResolved(resolved)
}

func normalizeChannelRef(ref string) string {
	ref = strings.TrimSpace(ref)
	ref = strings.TrimPrefix(ref, "@")
	for _, prefix := range []string{"https://t.me/", "http://t.me/", "t.me/"} {
		if strings.HasPrefix(ref, prefix) {
			ref = strings.TrimPrefix(ref, prefix)
			break
		}
	}
	ref = strings.Trim(ref, "/")
	if idx := strings.IndexAny(ref, "/?"); idx >= 0 {
		ref = ref[:idx]
	}
	return ref
}

func inputPeerFromResolved(resolved *tg.ContactsResolvedPeer) (tg.InputPeerClass, error) {
	if resolved == nil {
		return nil, errors.New("telegram resolved peer is empty")
	}
	switch peer := resolved.Peer.(type) {
	case *tg.PeerChannel:
		for _, chat := range resolved.Chats {
			if channel, ok := chat.(*tg.Channel); ok && channel.GetID() == peer.GetChannelID() {
				accessHash, ok := channel.GetAccessHash()
				if !ok {
					return nil, errors.New("telegram channel access hash is missing")
				}
				return &tg.InputPeerChannel{ChannelID: peer.GetChannelID(), AccessHash: accessHash}, nil
			}
			if channel, ok := chat.(*tg.ChannelForbidden); ok && channel.GetID() == peer.GetChannelID() {
				return &tg.InputPeerChannel{ChannelID: peer.GetChannelID(), AccessHash: channel.GetAccessHash()}, nil
			}
		}
	case *tg.PeerChat:
		return &tg.InputPeerChat{ChatID: peer.GetChatID()}, nil
	case *tg.PeerUser:
		for _, userClass := range resolved.Users {
			if user, ok := userClass.(*tg.User); ok && user.GetID() == peer.GetUserID() {
				accessHash, ok := user.GetAccessHash()
				if !ok {
					return nil, errors.New("telegram user access hash is missing")
				}
				return &tg.InputPeerUser{UserID: peer.GetUserID(), AccessHash: accessHash}, nil
			}
		}
	}
	return nil, errors.New("telegram resolved peer is unsupported")
}

func messagesFromHistory(history tg.MessagesMessagesClass) []Message {
	if history == nil {
		return nil
	}
	modified, ok := history.AsModified()
	if !ok {
		return nil
	}
	out := make([]Message, 0, len(modified.GetMessages()))
	for _, messageClass := range modified.GetMessages() {
		message, ok := messageClass.(*tg.Message)
		if !ok {
			continue
		}
		out = append(out, Message{
			ID:   int64(message.GetID()),
			Date: int64(message.GetDate()),
			Text: message.GetMessage(),
		})
	}
	return out
}

func convertFloodWait(err error) error {
	if err == nil {
		return nil
	}
	wait, ok := gotdtelegram.AsFloodWait(err)
	if !ok {
		return err
	}
	seconds := int(wait / time.Second)
	if seconds <= 0 {
		seconds = 1
	}
	return FloodWaitError{Seconds: seconds}
}
