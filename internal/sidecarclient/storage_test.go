package sidecarclient

import (
	"context"
	"testing"

	"github.com/xmm2022/echo/internal/sidecarclient/fakesidecar"
)

func TestListStorages(t *testing.T) {
	fake := fakesidecar.New(t, fakesidecar.Options{Version: "sidecar-abc123"})
	client := New(testConfig(fake.URL(), "sidecar-abc123"))

	got, err := client.ListStorages(context.Background())
	if err != nil {
		t.Fatalf("ListStorages returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListStorages returned %d storages, want 2", len(got))
	}
	if got[0].ID != "115-main" || got[0].MountPath != "/115-main" || got[0].Provider != "115" {
		t.Fatalf("unexpected first storage: %#v", got[0])
	}
}
