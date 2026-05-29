package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadPathAppliesEnvAndValidatesRoots(t *testing.T) {
	tmp := t.TempDir()
	workdir := filepath.Join(tmp, "work")
	secrets := filepath.Join(tmp, "secrets")
	manual := filepath.Join(tmp, "manual")
	for _, dir := range []string{workdir, secrets, manual} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("ECHO_ADMIN_TOKEN", "admin")
	t.Setenv("ECHO_MANUAL_IMPORT_ROOTS", manual)
	t.Setenv("ECHO_SERVER_BIND", ":18080")

	path := filepath.Join(tmp, "config.yaml")
	writeConfig(t, path, workdir, secrets)

	cfg, err := LoadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Bind != ":18080" {
		t.Fatalf("expected env bind override, got %q", cfg.Server.Bind)
	}
	if cfg.Auth.AdminToken != "admin" {
		t.Fatalf("expected env-expanded admin token, got %q", cfg.Auth.AdminToken)
	}
	if len(cfg.ManualImportRoots) != 1 || cfg.ManualImportRoots[0] != manual {
		t.Fatalf("unexpected manual import roots: %#v", cfg.ManualImportRoots)
	}
}

func TestLoadPathRejectsRelativeManualImportRoot(t *testing.T) {
	tmp := t.TempDir()
	workdir := filepath.Join(tmp, "work")
	secrets := filepath.Join(tmp, "secrets")
	for _, dir := range []string{workdir, secrets} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("ECHO_ADMIN_TOKEN", "admin")
	t.Setenv("ECHO_MANUAL_IMPORT_ROOTS", "relative")

	path := filepath.Join(tmp, "config.yaml")
	writeConfig(t, path, workdir, secrets)

	_, err := LoadPath(path)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "manual_import_roots[0] must be an absolute path") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func writeConfig(t *testing.T, path, workdir, secrets string) {
	t.Helper()
	body := fmt.Sprintf(`
server:
  bind: ":8080"
  read_timeout: 30s
  write_timeout: 60s
database:
  path: %s
auth:
  admin_token: "${ECHO_ADMIN_TOKEN}"
sidecar:
  default:
    base_url: http://sidecar:5244
    auth_token_env: ECHO_SIDECAR_TOKEN
    min_version: "test-version"
    request_timeout: 60s
    stream_timeout: 10m
producer:
  workdir_root: %s
  secrets_root: %s
  default_timeout: 6h
  tools:
    "115share2cas":
      binary: /usr/local/bin/115share2cas
      api_args_allowlist: ["share_url"]
manual_import_roots: []
jobs:
  max_concurrent: 4
  worker_per_job: 4
echo_output_defaults:
  kind: local
  base_path: %s
log:
  level: info
  format: json
`, filepath.Join(filepath.Dir(workdir), "echo.db"), workdir, secrets, filepath.Join(filepath.Dir(workdir), "output"))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
