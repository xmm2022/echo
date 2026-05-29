package fakesidecar

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

func TestFakePutCASRejectsNonCASSuffix(t *testing.T) {
	fake := New(t, Options{Version: "sidecar-abc123"})

	req, err := http.NewRequest(http.MethodPut, fake.URL()+"/api/fs/put", bytes.NewReader([]byte("payload")))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("File-Path", "/Movies/Film.mkv")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %q, want 400", resp.StatusCode, string(body))
	}
}
