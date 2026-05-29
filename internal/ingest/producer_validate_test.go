package ingest

import (
	"errors"
	"strings"
	"testing"
)

func validateTestConfig() ProducerConfig {
	return ProducerConfig{
		Tools: map[string]ProducerToolConfig{
			producerTool115Share2CAS: {
				Binary: "/usr/local/bin/115share2cas",
				APIArgsAllowlist: []string{
					"share_url", "share_code", "receive_code", "cookie_file",
					"mode", "batch_size", "temp_parent_cid",
					"recycle_password_file", "keep_temp", "limit",
				},
			},
		},
	}
}

func TestValidateProducerRequest(t *testing.T) {
	cfg := validateTestConfig()
	tests := []struct {
		name    string
		tool    string
		args    map[string]any
		wantErr bool
	}{
		{
			name: "valid transfer-batch with refs",
			tool: producerTool115Share2CAS,
			args: map[string]any{
				"share_url":             "https://115.com/s/x",
				"cookie_file":           "ref:cookies/115.txt",
				"recycle_password_file": "ref:pw/115.txt",
			},
		},
		{
			name: "valid direct mode needs no cookie",
			tool: producerTool115Share2CAS,
			args: map[string]any{"share_url": "https://115.com/s/x", "mode": "direct"},
		},
		{name: "disallowed tool", tool: "rm-rf", args: map[string]any{"share_url": "x", "mode": "direct"}, wantErr: true},
		{name: "unknown arg key", tool: producerTool115Share2CAS, args: map[string]any{"share_url": "x", "mode": "direct", "evil": "1"}, wantErr: true},
		{name: "share_url and share_code exclusive", tool: producerTool115Share2CAS, args: map[string]any{"share_url": "x", "share_code": "y", "mode": "direct"}, wantErr: true},
		{name: "missing share source", tool: producerTool115Share2CAS, args: map[string]any{"mode": "direct"}, wantErr: true},
		{name: "bad arg type", tool: producerTool115Share2CAS, args: map[string]any{"share_url": "x", "mode": "direct", "limit": "not-a-number"}, wantErr: true},
		{name: "cookie_file without ref", tool: producerTool115Share2CAS, args: map[string]any{"share_url": "x", "cookie_file": "/etc/passwd"}, wantErr: true},
		{name: "cookie_file absolute ref", tool: producerTool115Share2CAS, args: map[string]any{"share_url": "x", "cookie_file": "ref:/etc/passwd"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateProducerRequest(tt.tool, tt.args, cfg)
			if tt.wantErr {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				if !errors.Is(err, ErrProducerUnauthorized) {
					t.Fatalf("err = %v, want ErrProducerUnauthorized", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("want nil, got %v", err)
			}
		})
	}
}

func TestValidateProducerRequestNoFilesystemAccess(t *testing.T) {
	// A ref to a non-existent path must still pass pre-flight; the file is resolved
	// only at job-run time (RunProducer), never in the synchronous handler check.
	cfg := validateTestConfig()
	err := ValidateProducerRequest(producerTool115Share2CAS, map[string]any{
		"share_url":   "https://115.com/s/x",
		"cookie_file": "ref:does/not/exist.txt",
		"keep_temp":   true,
	}, cfg)
	if err != nil {
		t.Fatalf("pre-flight must not touch fs, got %v", err)
	}
}

func TestRedactArgs(t *testing.T) {
	in := map[string]any{
		"share_url":             "https://115.com/s/x?password=s3cret&t=ok",
		"receive_code":          "abcd",
		"cookie_file":           "ref:cookies/115.txt",
		"recycle_password_file": "ref:pw/115.txt",
		"mode":                  "direct",
	}
	out := RedactArgs(in)

	// Input is not mutated.
	if in["receive_code"] != "abcd" {
		t.Fatal("RedactArgs mutated its input")
	}
	if got := out["share_url"].(string); strings.Contains(got, "s3cret") {
		t.Fatalf("share_url still leaks password: %q", got)
	}
	if out["receive_code"] != "<redacted>" {
		t.Fatalf("receive_code = %v, want <redacted>", out["receive_code"])
	}
	if out["cookie_file"] != "<redacted-secret-path>" || out["recycle_password_file"] != "<redacted-secret-path>" {
		t.Fatalf("secret file refs not redacted: %v / %v", out["cookie_file"], out["recycle_password_file"])
	}
	if out["mode"] != "direct" {
		t.Fatalf("non-secret arg mode altered: %v", out["mode"])
	}
	if RedactArgs(nil) != nil {
		t.Fatal("RedactArgs(nil) should be nil")
	}
}