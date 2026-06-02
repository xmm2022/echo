package dispatch

import (
	"errors"
	"testing"

	"github.com/xmm2022/echo/internal/ingest"
)

func TestBuildProducerPayloadFor115(t *testing.T) {
	profile := ProducerProfile{
		LibraryID:            1,
		Provider:             "115",
		Tool:                 "115share2cas",
		TargetAccount:        "acc-115",
		TargetSubdirTemplate: "Movies/{{.Year}}",
		DefaultArgs: map[string]any{
			"cookie_file":           "ref:cookies/115.txt",
			"recycle_password_file": "ref:115/recycle.txt",
			"mode":                  "transfer-batch",
		},
	}
	res := Resource{ShareURL: "https://115.com/s/abc?password=pass", Title: "Movie", Year: 2024}
	payload, err := BuildProducerPayload(profile, res)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Tool != "115share2cas" || payload.TargetAccount != "acc-115" {
		t.Fatalf("payload = %#v", payload)
	}
	if payload.Args["share_url"] == "" {
		t.Fatalf("missing share_url: %#v", payload.Args)
	}
}

func TestRejectUnsupportedProvider(t *testing.T) {
	_, err := BuildProducerPayload(ProducerProfile{Provider: "139", Tool: "cas139"}, Resource{})
	if err == nil {
		t.Fatal("expected unsupported provider rejection")
	}
}

func TestBuildProducerPayloadRejectsUnsupportedTool(t *testing.T) {
	_, err := BuildProducerPayload(ProducerProfile{Provider: "115", Tool: "other"}, Resource{
		ShareURL: "https://115.com/s/abc",
	})
	if err == nil {
		t.Fatal("expected unsupported tool rejection")
	}
}

func TestBuildProducerPayloadRendersTargetSubdirTemplate(t *testing.T) {
	payload, err := BuildProducerPayload(ProducerProfile{
		LibraryID:            7,
		Provider:             "115",
		Tool:                 "115share2cas",
		TargetAccount:        "acc-115",
		TargetSubdirTemplate: "Movies/./{{.Year}}/{{.Title}}",
		DefaultArgs:          map[string]any{"mode": "direct"},
	}, Resource{ShareURL: "https://115.com/s/abc", Title: "Movie", Year: 2024})
	if err != nil {
		t.Fatal(err)
	}
	if payload.LibraryID != 7 {
		t.Fatalf("LibraryID = %d, want 7", payload.LibraryID)
	}
	if payload.TargetSubdir != "Movies/2024/Movie" {
		t.Fatalf("TargetSubdir = %q", payload.TargetSubdir)
	}
}

func TestBuildProducerPayloadRejectsEmptyTargetSubdir(t *testing.T) {
	_, err := BuildProducerPayload(ProducerProfile{
		Provider: "115",
		Tool:     "115share2cas",
	}, Resource{ShareURL: "https://115.com/s/abc"})
	if err == nil {
		t.Fatal("expected empty target subdir rejection")
	}
}

func TestBuildProducerPayloadRejectsInvalidTargetSubdir(t *testing.T) {
	_, err := BuildProducerPayload(ProducerProfile{
		Provider:             "115",
		Tool:                 "115share2cas",
		TargetSubdirTemplate: "../{{.Title}}",
	}, Resource{ShareURL: "https://115.com/s/abc", Title: "Movie"})
	if err == nil {
		t.Fatal("expected invalid target subdir rejection")
	}
}

func TestBuildProducerPayloadCopiesDefaultArgs(t *testing.T) {
	defaultArgs := map[string]any{
		"mode":  "direct",
		"limit": 3,
	}
	payload, err := BuildProducerPayload(ProducerProfile{
		Provider:             "115",
		Tool:                 "115share2cas",
		TargetAccount:        "acc-115",
		TargetSubdirTemplate: "Movies",
		DefaultArgs:          defaultArgs,
	}, Resource{ShareURL: "https://115.com/s/abc"})
	if err != nil {
		t.Fatal(err)
	}
	payload.Args["mode"] = "transfer-batch"
	payload.Args["limit"] = 10

	if defaultArgs["mode"] != "direct" || defaultArgs["limit"] != 3 {
		t.Fatalf("DefaultArgs mutated: %#v", defaultArgs)
	}
	if _, ok := defaultArgs["share_url"]; ok {
		t.Fatalf("DefaultArgs gained share_url: %#v", defaultArgs)
	}
}

func TestBuildProducerPayloadRejectsProfileShareArgs(t *testing.T) {
	for _, key := range []string{"share_url", "share_code", "receive_code"} {
		t.Run(key, func(t *testing.T) {
			_, err := BuildProducerPayload(ProducerProfile{
				Provider:    "115",
				Tool:        "115share2cas",
				DefaultArgs: map[string]any{key: "from-profile"},
			}, Resource{ShareURL: "https://115.com/s/abc"})
			if err == nil {
				t.Fatal("expected profile share arg rejection")
			}
		})
	}
}

func TestBuildProducerPayloadPrefersShareCodeWhenComplete(t *testing.T) {
	payload, err := BuildProducerPayload(ProducerProfile{
		Provider:             "115",
		Tool:                 "115share2cas",
		TargetSubdirTemplate: "Movies",
		DefaultArgs:          map[string]any{"mode": "direct"},
	}, Resource{
		ShareURL:    "https://115.com/s/abc",
		ShareCode:   "abc",
		ReceiveCode: "pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := payload.Args["share_url"]; ok {
		t.Fatalf("unexpected share_url: %#v", payload.Args)
	}
	if payload.Args["share_code"] != "abc" || payload.Args["receive_code"] != "pass" {
		t.Fatalf("share code args = %#v", payload.Args)
	}
}

func TestBuildProducerPayloadUsesShareCodeWhenURLMissing(t *testing.T) {
	payload, err := BuildProducerPayload(ProducerProfile{
		Provider:             "115",
		Tool:                 "115share2cas",
		TargetSubdirTemplate: "Movies",
		DefaultArgs:          map[string]any{"mode": "direct"},
	}, Resource{ShareCode: "abc", ReceiveCode: "pass"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := payload.Args["share_url"]; ok {
		t.Fatalf("unexpected share_url: %#v", payload.Args)
	}
	if payload.Args["share_code"] != "abc" || payload.Args["receive_code"] != "pass" {
		t.Fatalf("share code args = %#v", payload.Args)
	}
}

func TestBuildProducerPayloadRejectsMissingShareSource(t *testing.T) {
	_, err := BuildProducerPayload(ProducerProfile{
		Provider: "115",
		Tool:     "115share2cas",
	}, Resource{ShareCode: "abc"})
	if err == nil {
		t.Fatal("expected missing share source rejection")
	}
}

func TestValidatePayloadDelegatesToIngestValidation(t *testing.T) {
	cfg := ingest.ProducerConfig{
		Tools: map[string]ingest.ProducerToolConfig{
			"115share2cas": {
				Binary: "/usr/local/bin/115share2cas",
				APIArgsAllowlist: []string{
					"share_url", "share_code", "receive_code", "cookie_file",
					"mode", "recycle_password_file",
				},
			},
		},
	}
	payload, err := BuildProducerPayload(ProducerProfile{
		Provider:             "115",
		Tool:                 "115share2cas",
		TargetSubdirTemplate: "Movies",
		DefaultArgs: map[string]any{
			"cookie_file":           "ref:cookies/115.txt",
			"recycle_password_file": "ref:115/recycle.txt",
			"mode":                  "transfer-batch",
		},
	}, Resource{ShareURL: "https://115.com/s/abc"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePayload(payload, cfg); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}

	payload.Args["unexpected"] = "value"
	err = ValidatePayload(payload, cfg)
	if !errors.Is(err, ingest.ErrProducerUnauthorized) {
		t.Fatalf("ValidatePayload error = %v, want ErrProducerUnauthorized", err)
	}
}
