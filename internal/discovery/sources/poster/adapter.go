package poster

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/xmm2022/echo/internal/discovery"
)

type AdapterConfig struct {
	AllowedDomains []string
}

type Resource struct {
	Provider    string
	ShareURL    string
	ShareCode   string
	ReceiveCode string
	Title       string
	RawText     string
}

type Adapter struct {
	client *SafeClient
}

type crawlConfig struct {
	URLs []string `json:"urls"`
}

var anchorPattern = regexp.MustCompile(`(?is)<a\b[^>]*\bhref\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))[^>]*>(.*?)</a>`)
var tagPattern = regexp.MustCompile(`(?is)<[^>]+>`)
var querySecretPattern = regexp.MustCompile(`(?i)([?&])(?:password|pwd|receive_code|receiveCode)=[^\s&#]+`)
var textSecretPattern = regexp.MustCompile(`(?i)\b(?:password|pwd|receive_code|receiveCode)\s*=\s*[^\s&]+`)

func NewAdapter(cfg AdapterConfig) *Adapter {
	return &Adapter{
		client: NewSafeClient(SafeConfig{AllowedDomains: cfg.AllowedDomains}),
	}
}

func (a *Adapter) CrawlURL(ctx context.Context, rawURL string) ([]Resource, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("poster crawl: HTTP status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return ParseHTML(rawURL, body)
}

func (a *Adapter) Crawl(ctx context.Context, source discovery.Source) (discovery.SourceCrawlResult, error) {
	var cfg crawlConfig
	if err := json.Unmarshal([]byte(source.ConfigText()), &cfg); err != nil {
		return discovery.SourceCrawlResult{}, fmt.Errorf("poster crawl: parse source config: %w", err)
	}
	out := discovery.SourceCrawlResult{}
	for _, rawURL := range cfg.URLs {
		resources, err := a.CrawlURL(ctx, rawURL)
		if err != nil {
			return discovery.SourceCrawlResult{}, fmt.Errorf("poster crawl %s: %w", rawURL, err)
		}
		for _, resource := range resources {
			out.Items = append(out.Items, toParsedResource(resource))
		}
	}
	return out, nil
}

func ParseHTML(baseURL string, body []byte) ([]Resource, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	matches := anchorPattern.FindAllSubmatch(body, -1)
	items := make([]Resource, 0, len(matches))
	for _, match := range matches {
		href := firstNonEmptySubmatch(match[1], match[2], match[3])
		link, err := parse115URL(base, html.UnescapeString(href))
		if err != nil {
			continue
		}
		receiveCode := receiveCodeFromURL(link)
		title := redactReceiveCode(redactAnchorText(cleanAnchorText(string(match[4]))), receiveCode)
		items = append(items, Resource{
			Provider:    "115",
			ShareURL:    redactedShareURL(link),
			ShareCode:   shareCodeFromURL(link),
			ReceiveCode: receiveCode,
			Title:       title,
			RawText:     title,
		})
	}
	return items, nil
}

func toParsedResource(resource Resource) discovery.ParsedResource {
	parsedJSON, _ := json.Marshal(map[string]string{
		"provider":           resource.Provider,
		"link_kind":          string(discovery.Link115Share),
		"title":              resource.Title,
		"share_url_redacted": resource.ShareURL,
	})
	return discovery.ParsedResource{
		Provider:         discovery.Provider115,
		LinkKind:         discovery.Link115Share,
		ExternalKey:      "115:" + resource.ShareCode,
		Title:            resource.Title,
		ShareCode:        resource.ShareCode,
		ReceiveCode:      resource.ReceiveCode,
		ShareURLRedacted: resource.ShareURL,
		RawText:          []byte(resource.RawText),
		RawTextRedacted:  resource.RawText,
		FeatureJSON:      "{}",
		ParsedJSON:       string(parsedJSON),
		ObservedAt:       time.Now(),
	}
}

func parse115URL(base *url.URL, href string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return nil, err
	}
	parsed = base.ResolveReference(parsed)
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

func redactedShareURL(link *url.URL) string {
	out := *link
	out.RawQuery = ""
	out.Fragment = ""
	return out.String()
}

func cleanAnchorText(value string) string {
	value = tagPattern.ReplaceAllString(value, " ")
	value = html.UnescapeString(value)
	return strings.Join(strings.Fields(value), " ")
}

func redactAnchorText(value string) string {
	value = querySecretPattern.ReplaceAllString(value, "$1[REDACTED]")
	return textSecretPattern.ReplaceAllString(value, "[REDACTED]")
}

func redactReceiveCode(value, receiveCode string) string {
	if receiveCode == "" {
		return value
	}
	return strings.ReplaceAll(value, receiveCode, "[REDACTED]")
}

func firstNonEmptySubmatch(values ...[]byte) string {
	for _, value := range values {
		if len(value) > 0 {
			return string(value)
		}
	}
	return ""
}
