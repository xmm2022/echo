package telegram

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/xmm2022/echo/internal/config"
	"github.com/xmm2022/echo/internal/discovery"
)

type FakeClient struct {
	Messages         []Message
	FloodWaitSeconds int
	SessionRef       string
	ChannelRef       string
	AfterMessageID   int64
	Limit            int
}

func (f *FakeClient) History(ctx context.Context, sessionRef string, channelRef string, afterMessageID int64, limit int) ([]Message, error) {
	if f.FloodWaitSeconds > 0 {
		return nil, FloodWaitError{Seconds: f.FloodWaitSeconds}
	}
	f.SessionRef = sessionRef
	f.ChannelRef = channelRef
	f.AfterMessageID = afterMessageID
	f.Limit = limit
	return f.Messages, nil
}

func TestCursorAdvancesAfterProcessing(t *testing.T) {
	client := &FakeClient{Messages: []Message{{ID: 10, Text: "Movie https://115.com/s/abc?password=pass"}}}
	cursor := Cursor{LastMessageID: 9}
	adapter := NewAdapter(client)
	items, next, err := adapter.CrawlChannel(context.Background(), Channel{Ref: "test", SessionRef: "ref:session.json", Cursor: cursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || next.LastMessageID != 10 {
		t.Fatalf("items=%#v next=%#v", items, next)
	}
}

func TestFloodWaitReturnsBackoff(t *testing.T) {
	client := &FakeClient{FloodWaitSeconds: 60}
	_, _, err := NewAdapter(client).CrawlChannel(context.Background(), Channel{Ref: "test", SessionRef: "ref:session.json"})
	var flood FloodWaitError
	if !errors.As(err, &flood) || flood.Seconds != 60 {
		t.Fatalf("err = %v, want FloodWaitError", err)
	}
}

func TestCrawlConvertsConfigAndRedactsParsedJSON(t *testing.T) {
	client := &FakeClient{Messages: []Message{{ID: 10, Date: 100, Text: "Movie code abc receive secret https://115.com/s/abc?receiveCode=secret"}}}
	source := discovery.Source{
		ConfigJson: `{"channels":[{"ref":"@test","session_ref":"ref:session.json","last_message_id":9,"last_message_date":90}]}`,
	}
	result, err := NewAdapter(client).Crawl(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if client.SessionRef != "ref:session.json" || client.ChannelRef != "@test" || client.AfterMessageID != 9 {
		t.Fatalf("history args session=%q channel=%q after=%d", client.SessionRef, client.ChannelRef, client.AfterMessageID)
	}
	if len(result.Items) != 1 {
		t.Fatalf("items=%#v", result.Items)
	}
	item := result.Items[0]
	if item.ShareCode != "abc" || item.ReceiveCode != "secret" {
		t.Fatalf("share=%q receive=%q", item.ShareCode, item.ReceiveCode)
	}
	if strings.Contains(item.Title, "secret") || strings.Contains(item.Title, "abc") {
		t.Fatalf("unredacted title: %q", item.Title)
	}
	if strings.Contains(item.RawTextRedacted, "secret") || strings.Contains(item.RawTextRedacted, "abc") {
		t.Fatalf("unredacted title/raw redacted: title=%q raw=%q", item.Title, item.RawTextRedacted)
	}
	if strings.Contains(item.ParsedJSON, "secret") || strings.Contains(item.ParsedJSON, "abc") || strings.Contains(item.ParsedJSON, "https://115.com") {
		t.Fatalf("parsed_json contains sensitive or raw text: %s", item.ParsedJSON)
	}
	if err := discovery.ValidateParsedJSONForStorage([]byte(item.ParsedJSON)); err != nil {
		t.Fatal(err)
	}
}

func TestExternalKeyUsesLinkIndexWithoutShareCode(t *testing.T) {
	client := &FakeClient{Messages: []Message{{ID: 10, Text: "Links https://115.com/s/abc?pwd=one https://115.com/s/def?pwd=two"}}}
	items, _, err := NewAdapter(client).CrawlChannel(context.Background(), Channel{Ref: "test", SessionRef: "ref:session.json"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items=%#v", items)
	}
	if items[0].ExternalKey != "tg:test:10:115:0" || items[1].ExternalKey != "tg:test:10:115:1" {
		t.Fatalf("external keys = %q, %q", items[0].ExternalKey, items[1].ExternalKey)
	}
	for _, item := range items {
		if strings.Contains(item.ExternalKey, item.ShareCode) {
			t.Fatalf("external key %q contains share code %q", item.ExternalKey, item.ShareCode)
		}
	}
}

func TestHistorySessionResolveErrorIsSanitized(t *testing.T) {
	root := t.TempDir()
	_, err := NewMTProtoClient(root, 1, "hash").History(context.Background(), "ref:missing/session.json", "@test", 0, 1)
	if err == nil {
		t.Fatal("expected error")
	}
	assertNoSensitiveErrorText(t, err.Error(), root, "missing/session.json", "lstat")
	if err.Error() != "telegram history: resolve session ref failed" {
		t.Fatalf("err = %q", err.Error())
	}
}

func TestAPIHashResolveErrorIsSanitized(t *testing.T) {
	root := t.TempDir()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	client := NewMTProtoClientFromConfig(config.TelegramConfig{
		SessionRoot: root,
		APIID:       1,
		APIHashRef:  "ref:missing/hash.txt",
	}, root, logger)
	_, err := client.History(context.Background(), "ref:session.json", "@test", 0, 1)
	if err == nil {
		t.Fatal("expected error")
	}
	assertNoSensitiveErrorText(t, err.Error(), root, "missing/hash.txt", "lstat")
	if err.Error() != "telegram config: resolve api hash failed" {
		t.Fatalf("err = %q", err.Error())
	}
	assertNoSensitiveErrorText(t, logs.String(), root, "missing/hash.txt", "lstat")
}

func TestEmptyHistoryPreservesCursor(t *testing.T) {
	cursor := Cursor{LastMessageID: 9, LastMessageDate: 90}
	items, next, err := NewAdapter(&FakeClient{}).CrawlChannel(context.Background(), Channel{Ref: "test", SessionRef: "ref:session.json", Cursor: cursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 || next != cursor {
		t.Fatalf("items=%#v next=%#v", items, next)
	}
}

func assertNoSensitiveErrorText(t *testing.T, text string, values ...string) {
	t.Helper()
	for _, value := range values {
		if value != "" && strings.Contains(text, value) {
			t.Fatalf("text %q contains sensitive value %q", text, value)
		}
	}
}
