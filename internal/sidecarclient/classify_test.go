package sidecarclient

import (
	"strings"
	"testing"
)

// TestSafeSidecarMessage pins the redaction/stripping contract that the typed
// error path (notably the /d/ HTML snippet in Stream) relies on: HTML tags are
// stripped, credential fragments are redacted, and the result is capped. It
// asserts the cleaned output never carries a secret value or an angle-bracket
// tag.
func TestSafeSidecarMessage(t *testing.T) {
	tests := []struct {
		name        string
		in          string
		wantSubstr  []string // must appear in the cleaned output
		notSubstr   []string // must NOT appear (secret values, raw tags)
		wantMaxSize int      // 0 means no size assertion
	}{
		{
			name:       "strips html tags",
			in:         "<html><body>failed to get file: object not found</body></html>",
			wantSubstr: []string{"failed to get file: object not found"},
			notSubstr:  []string{"<html>", "<body>", "</body>", "</html>", "<", ">"},
		},
		{
			name:       "redacts token kv",
			in:         "download error token=abc123secret retry later",
			wantSubstr: []string{"[redacted]", "download error", "retry later"},
			notSubstr:  []string{"abc123secret", "token=abc123secret"},
		},
		{
			name:       "redacts sign kv",
			in:         "url rejected sign=deadbeefSIGN done",
			wantSubstr: []string{"[redacted]"},
			notSubstr:  []string{"deadbeefSIGN", "sign=deadbeefSIGN"},
		},
		{
			name:       "redacts signature kv",
			in:         "bad request signature=ZZZsignatureZZZ end",
			wantSubstr: []string{"[redacted]"},
			notSubstr:  []string{"ZZZsignatureZZZ", "signature=ZZZsignatureZZZ"},
		},
		{
			name:       "redacts authorization header",
			in:         "Authorization: Bearer topsecrettoken",
			wantSubstr: []string{"[redacted]"},
			notSubstr:  []string{"topsecrettoken", "Bearer topsecrettoken"},
		},
		{
			name:       "redacts cookie header",
			in:         "Cookie: session=supersecretcookievalue",
			wantSubstr: []string{"[redacted]"},
			notSubstr:  []string{"supersecretcookievalue"},
		},
		{
			name: "redacts multiple secrets in one string",
			// HTML tag + two KV secrets on one line, then an Authorization header on
			// its own line (the header pattern is greedy to end-of-line).
			in:         "<p>fail token=AAAtokenAAA sign=BBBsignBBB</p>\nAuthorization: Bearer CCCauthCCC",
			wantSubstr: []string{"[redacted]"},
			notSubstr:  []string{"AAAtokenAAA", "BBBsignBBB", "CCCauthCCC", "<p>", "</p>", "<", ">"},
		},
		{
			name:        "caps at 512 bytes",
			in:          strings.Repeat("A", 4096),
			wantMaxSize: 512,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := safeSidecarMessage(tt.in)
			for _, want := range tt.wantSubstr {
				if !strings.Contains(got, want) {
					t.Errorf("safeSidecarMessage(%q) = %q, want it to contain %q", tt.in, got, want)
				}
			}
			for _, bad := range tt.notSubstr {
				if strings.Contains(got, bad) {
					t.Errorf("safeSidecarMessage(%q) = %q, leaked %q", tt.in, got, bad)
				}
			}
			if tt.wantMaxSize > 0 && len(got) > tt.wantMaxSize {
				t.Errorf("safeSidecarMessage(%q) len = %d, want <= %d", tt.in, len(got), tt.wantMaxSize)
			}
		})
	}
}
