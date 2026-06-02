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
	t.Setenv("ECHO_BOOTSTRAP_ADMIN_TOKEN", "boot")
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
	if cfg.Auth.BootstrapAdminToken != "boot" {
		t.Fatalf("expected env-expanded bootstrap token, got %q", cfg.Auth.BootstrapAdminToken)
	}
	if len(cfg.ManualImportRoots) != 1 || cfg.ManualImportRoots[0] != manual {
		t.Fatalf("unexpected manual import roots: %#v", cfg.ManualImportRoots)
	}
}

func TestLoadPathDefaultsDisabledDiscovery(t *testing.T) {
	tmp := t.TempDir()
	workdir := filepath.Join(tmp, "work")
	secrets := filepath.Join(tmp, "secrets")
	for _, dir := range []string{workdir, secrets} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("ECHO_BOOTSTRAP_ADMIN_TOKEN", "boot")

	path := filepath.Join(tmp, "config.yaml")
	writeConfig(t, path, workdir, secrets)

	cfg, err := LoadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Discovery.Enabled {
		t.Fatal("discovery enabled by default, want disabled")
	}
	if cfg.Discovery.RawDebug.MaxBytes != 64*1024 {
		t.Fatalf("raw debug max bytes = %d, want 65536", cfg.Discovery.RawDebug.MaxBytes)
	}
	if cfg.Discovery.RawDebug.RetentionDays != 7 {
		t.Fatalf("raw debug retention days = %d, want 7", cfg.Discovery.RawDebug.RetentionDays)
	}
	if cfg.Discovery.MaxConcurrent != 2 {
		t.Fatalf("discovery max concurrent = %d, want 2", cfg.Discovery.MaxConcurrent)
	}
}

func TestLoadPathRejectsInvalidDiscoveryNumbers(t *testing.T) {
	cases := []struct {
		name    string
		suffix  string
		wantErr string
	}{
		{
			name: "negative raw debug max bytes",
			suffix: `
discovery:
  raw_debug:
    max_bytes: -1
`,
			wantErr: "discovery.raw_debug.max_bytes must be a positive integer",
		},
		{
			name: "negative raw debug retention days",
			suffix: `
discovery:
  raw_debug:
    retention_days: -1
`,
			wantErr: "discovery.raw_debug.retention_days must be a positive integer",
		},
		{
			name: "negative max concurrent",
			suffix: `
discovery:
  max_concurrent: -1
`,
			wantErr: "discovery.max_concurrent must be a positive integer",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			workdir := filepath.Join(tmp, "work")
			secrets := filepath.Join(tmp, "secrets")
			for _, dir := range []string{workdir, secrets} {
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("ECHO_BOOTSTRAP_ADMIN_TOKEN", "boot")

			path := filepath.Join(tmp, "config.yaml")
			writeConfigWithSuffix(t, path, workdir, secrets, tt.suffix)

			_, err := LoadPath(path)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadPathValidatesEnabledDiscovery(t *testing.T) {
	cases := []struct {
		name    string
		suffix  string
		wantErr string
	}{
		{
			name: "missing secrets root",
			suffix: `
discovery:
  enabled: true
  tmdb:
    api_key_ref: "env:TMDB_API_KEY"
  telegram:
    api_id: 1
    api_hash_ref: "env:TELEGRAM_API_HASH"
`,
			wantErr: "secrets_root is required when discovery is enabled",
		},
		{
			name: "bad tmdb ref",
			suffix: `
secrets_root: "/data/secrets"
discovery:
  enabled: true
  tmdb:
    api_key_ref: "not-a-ref"
  telegram:
    api_id: 1
    api_hash_ref: "env:TELEGRAM_API_HASH"
`,
			wantErr: "discovery.tmdb.api_key_ref",
		},
		{
			name: "missing telegram api id",
			suffix: `
secrets_root: "/data/secrets"
discovery:
  enabled: true
  tmdb:
    api_key_ref: "env:TMDB_API_KEY"
  telegram:
    api_id: 0
    api_hash_ref: "env:TELEGRAM_API_HASH"
`,
			wantErr: "discovery.telegram.api_id is required",
		},
		{
			name: "bad telegram hash ref",
			suffix: `
secrets_root: "/data/secrets"
discovery:
  enabled: true
  tmdb:
    api_key_ref: "env:TMDB_API_KEY"
  telegram:
    api_id: 1
    api_hash_ref: "not-a-ref"
`,
			wantErr: "discovery.telegram.api_hash_ref",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			workdir := filepath.Join(tmp, "work")
			secrets := filepath.Join(tmp, "secrets")
			for _, dir := range []string{workdir, secrets} {
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("ECHO_BOOTSTRAP_ADMIN_TOKEN", "boot")

			path := filepath.Join(tmp, "config.yaml")
			writeConfigWithSuffix(t, path, workdir, secrets, tt.suffix)

			_, err := LoadPath(path)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadPathAppliesDiscoveryEnvOverrides(t *testing.T) {
	tmp := t.TempDir()
	workdir := filepath.Join(tmp, "work")
	secrets := filepath.Join(tmp, "secrets")
	for _, dir := range []string{workdir, secrets} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("ECHO_BOOTSTRAP_ADMIN_TOKEN", "boot")
	t.Setenv("ECHO_DISCOVERY_TMDB_API_KEY_REF", "env:OVERRIDE_TMDB")
	t.Setenv("ECHO_DISCOVERY_TELEGRAM_SESSION_ROOT", "/override/telegram")
	t.Setenv("ECHO_DISCOVERY_TELEGRAM_API_HASH_REF", "env:OVERRIDE_HASH")
	t.Setenv("ECHO_DISCOVERY_RAW_DEBUG_STORAGE_ROOT", "/override/raw")

	path := filepath.Join(tmp, "config.yaml")
	writeConfigWithSuffix(t, path, workdir, secrets, `
discovery:
  raw_debug:
    storage_root: "/data/raw"
  tmdb:
    api_key_ref: "env:TMDB_API_KEY"
  telegram:
    session_root: "/data/telegram"
    api_hash_ref: "env:TELEGRAM_API_HASH"
`)

	cfg, err := LoadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Discovery.TMDB.APIKeyRef != "env:OVERRIDE_TMDB" {
		t.Fatalf("tmdb api key ref = %q", cfg.Discovery.TMDB.APIKeyRef)
	}
	if cfg.Discovery.Telegram.SessionRoot != "/override/telegram" {
		t.Fatalf("telegram session root = %q", cfg.Discovery.Telegram.SessionRoot)
	}
	if cfg.Discovery.Telegram.APIHashRef != "env:OVERRIDE_HASH" {
		t.Fatalf("telegram api hash ref = %q", cfg.Discovery.Telegram.APIHashRef)
	}
	if cfg.Discovery.RawDebug.StorageRoot != "/override/raw" {
		t.Fatalf("raw debug storage root = %q", cfg.Discovery.RawDebug.StorageRoot)
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
	t.Setenv("ECHO_BOOTSTRAP_ADMIN_TOKEN", "boot")
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

func TestV02ConfigLoadsEmbyProxyAndReadiness(t *testing.T) {
	// The fixture below pins producer.workdir_root / producer.secrets_root to
	// fixed /tmp paths (validate() requires them to be existing directories), so
	// make them deterministic instead of relying on host state.
	for _, dir := range []string{"/tmp/echo-work", "/tmp/echo-secrets"} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	path := writeTempConfig(t, `
server:
  bind: ":8080"
  read_timeout: 5s
  write_timeout: 5s
database:
  path: "/tmp/echo.db"
auth:
  bootstrap_admin_token: "boot"
sidecar:
  default:
    base_url: "http://sidecar:5244"
    auth_token_env: "ECHO_SIDECAR_TOKEN"
    min_version: "0.1.0"
    request_timeout: 5s
    stream_timeout: 30s
producer:
  workdir_root: "/tmp/echo-work"
  secrets_root: "/tmp/echo-secrets"
  default_timeout: 1m
  tools:
    "115share2cas":
      binary: /usr/local/bin/115share2cas
      api_args_allowlist: ["share_url"]
jobs:
  max_concurrent: 1
  worker_per_job: 1
echo_output_defaults:
  kind: "local"
  base_path: "/tmp/echo-output"
readiness:
  require_sidecar_contract: true
  require_sidecar_connectivity: false
  require_emby_connectivity: false
secrets_root: "/data/secrets"
emby_proxy:
  enabled: true
  config_sync: "seed_if_missing"
  public_base_url: "https://echo.example.com"
  proxy_prefix: "/emby"
  upstream:
    id: "default"
    name: "main"
    base_url: "http://emby:8096"
    api_key_ref: "env:EMBY_API_KEY"
  playback:
    session_ttl: "12h"
    max_candidate_copies: 5
    redact_mapped_path: true
    mapped_only: false
  path_mappings:
    - library_id: 1
      emby_path_prefix: "/media/movies"
      echo_rel_prefix: "movies"
      case_sensitive: true
`)
	cfg, err := LoadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.BootstrapAdminToken != "boot" || !cfg.Readiness.RequireSidecarContract {
		t.Fatalf("config = %+v, want v0.2 auth/readiness", cfg)
	}
	if cfg.EmbyProxy.ProxyPrefix != "/emby" || cfg.EmbyProxy.Upstream.BaseURL != "http://emby:8096" {
		t.Fatalf("emby proxy config = %+v", cfg.EmbyProxy)
	}
	if cfg.EmbyProxy.ConfigSync != "seed_if_missing" || cfg.EmbyProxy.Playback.MaxCandidateCopies != 5 ||
		cfg.EmbyProxy.Playback.RedactMappedPath == nil || !*cfg.EmbyProxy.Playback.RedactMappedPath {
		t.Fatalf("emby proxy runtime options = %+v", cfg.EmbyProxy)
	}
	if len(cfg.EmbyProxy.PathMappings) != 1 || cfg.EmbyProxy.PathMappings[0].EchoRelPrefix != "movies" {
		t.Fatalf("path mappings = %+v", cfg.EmbyProxy.PathMappings)
	}
}

func TestV02ConfigDefaultsRedactMappedPathToTrue(t *testing.T) {
	for _, dir := range []string{"/tmp/echo-work", "/tmp/echo-secrets"} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	path := writeTempConfig(t, `
server:
  bind: ":8080"
  read_timeout: 5s
  write_timeout: 5s
database:
  path: "/tmp/echo.db"
auth:
  bootstrap_admin_token: "boot"
sidecar:
  default:
    base_url: "http://sidecar:5244"
    auth_token_env: "ECHO_SIDECAR_TOKEN"
    min_version: "0.1.0"
    request_timeout: 5s
    stream_timeout: 30s
producer:
  workdir_root: "/tmp/echo-work"
  secrets_root: "/tmp/echo-secrets"
  default_timeout: 1m
  tools:
    "115share2cas":
      binary: /usr/local/bin/115share2cas
      api_args_allowlist: ["share_url"]
jobs:
  max_concurrent: 1
  worker_per_job: 1
echo_output_defaults:
  kind: "local"
  base_path: "/tmp/echo-output"
secrets_root: "/data/secrets"
emby_proxy:
  enabled: true
  public_base_url: "https://echo.example.com"
  proxy_prefix: "/emby"
  upstream:
    id: "default"
    name: "main"
    base_url: "http://emby:8096"
    api_key_ref: "env:EMBY_API_KEY"
  playback:
    session_ttl: "12h"
    max_candidate_copies: 5
`)
	cfg, err := LoadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EmbyProxy.Playback.RedactMappedPath == nil || !*cfg.EmbyProxy.Playback.RedactMappedPath {
		t.Fatalf("redact_mapped_path default = false, want true")
	}
}

func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeConfig(t *testing.T, path, workdir, secrets string) {
	t.Helper()
	writeConfigWithSuffix(t, path, workdir, secrets, "")
}

func writeConfigWithSuffix(t *testing.T, path, workdir, secrets, suffix string) {
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
  bootstrap_admin_token: "${ECHO_BOOTSTRAP_ADMIN_TOKEN}"
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
%s`, filepath.Join(filepath.Dir(workdir), "echo.db"), workdir, secrets, filepath.Join(filepath.Dir(workdir), "output"), suffix)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
