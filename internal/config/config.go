package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const DefaultPath = "/etc/echo/config.yaml"

type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("duration must be a scalar")
	}
	if node.Value == "" {
		d.Duration = 0
		return nil
	}
	parsed, err := time.ParseDuration(node.Value)
	if err != nil {
		return err
	}
	d.Duration = parsed
	return nil
}

func (d Duration) String() string {
	return d.Duration.String()
}

type Config struct {
	Server             ServerConfig       `yaml:"server"`
	Database           DatabaseConfig     `yaml:"database"`
	Auth               AuthConfig         `yaml:"auth"`
	Sidecar            SidecarConfig      `yaml:"sidecar"`
	Producer           ProducerConfig     `yaml:"producer"`
	ManualImportRoots  []string           `yaml:"manual_import_roots"`
	Jobs               JobsConfig         `yaml:"jobs"`
	EchoOutputDefaults EchoOutputDefaults `yaml:"echo_output_defaults"`
	Log                LogConfig          `yaml:"log"`
	Readiness          ReadyConfig        `yaml:"readiness"`
	SecretsRoot        string             `yaml:"secrets_root"`
	EmbyProxy          EmbyProxyConfig    `yaml:"emby_proxy"`
}

// ReadyConfig gates /readyz on v0.2 dependencies. It is distinct from the
// http.ReadyConfig wiring type: this one carries yaml tags and is the
// operator-facing surface; main.go maps it onto the http readiness checker.
type ReadyConfig struct {
	RequireSidecarContract     bool `yaml:"require_sidecar_contract"`
	RequireSidecarConnectivity bool `yaml:"require_sidecar_connectivity"`
	RequireEmbyConnectivity    bool `yaml:"require_emby_connectivity"`
}

type EmbyProxyConfig struct {
	Enabled       bool                    `yaml:"enabled"`
	ConfigSync    string                  `yaml:"config_sync"`
	PublicBaseURL string                  `yaml:"public_base_url"`
	ProxyPrefix   string                  `yaml:"proxy_prefix"`
	Upstream      EmbyUpstreamConfig      `yaml:"upstream"`
	Playback      EmbyPlaybackConfig      `yaml:"playback"`
	PathMappings  []EmbyPathMappingConfig `yaml:"path_mappings"`
}

type EmbyUpstreamConfig struct {
	ID        string `yaml:"id"`
	Name      string `yaml:"name"`
	BaseURL   string `yaml:"base_url"`
	APIKeyRef string `yaml:"api_key_ref"`
}

type EmbyPlaybackConfig struct {
	SessionTTL Duration `yaml:"session_ttl"`
	// MaxCandidateCopies is validated (1..20) but not yet threaded into the resolver,
	// which currently uses a hardcoded limit of 5 (this knob's documented default).
	// Follow-up: wire into playback.NewResolver/ResolveCopies.
	MaxCandidateCopies int   `yaml:"max_candidate_copies"`
	RedactMappedPath   *bool `yaml:"redact_mapped_path"`
	MappedOnly         bool  `yaml:"mapped_only"`
}

type EmbyPathMappingConfig struct {
	LibraryID      int64  `yaml:"library_id"`
	EmbyPathPrefix string `yaml:"emby_path_prefix"`
	EchoRelPrefix  string `yaml:"echo_rel_prefix"`
	CaseSensitive  bool   `yaml:"case_sensitive"`
}

type ServerConfig struct {
	Bind         string   `yaml:"bind"`
	ReadTimeout  Duration `yaml:"read_timeout"`
	WriteTimeout Duration `yaml:"write_timeout"`
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

type AuthConfig struct {
	AdminToken          string `yaml:"admin_token"`
	BootstrapAdminToken string `yaml:"bootstrap_admin_token"`
}

type SidecarConfig struct {
	Default SidecarEndpointConfig `yaml:"default"`
}

type SidecarEndpointConfig struct {
	BaseURL        string   `yaml:"base_url"`
	AuthTokenEnv   string   `yaml:"auth_token_env"`
	MinVersion     string   `yaml:"min_version"`
	RequestTimeout Duration `yaml:"request_timeout"`
	StreamTimeout  Duration `yaml:"stream_timeout"`
}

type ProducerConfig struct {
	WorkdirRoot    string                  `yaml:"workdir_root"`
	SecretsRoot    string                  `yaml:"secrets_root"`
	DefaultTimeout Duration                `yaml:"default_timeout"`
	Tools          map[string]ProducerTool `yaml:"tools"`
}

type ProducerTool struct {
	Binary           string   `yaml:"binary"`
	APIArgsAllowlist []string `yaml:"api_args_allowlist"`
}

type JobsConfig struct {
	MaxConcurrent int `yaml:"max_concurrent"`
	WorkerPerJob  int `yaml:"worker_per_job"`
}

type EchoOutputDefaults struct {
	Kind     string `yaml:"kind"`
	BasePath string `yaml:"base_path"`
}

type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

func Load() (*Config, error) {
	path := os.Getenv("ECHO_CONFIG_PATH")
	if path == "" {
		path = DefaultPath
	}
	return LoadPath(path)
}

func LoadPath(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}

	cfg.expandEnv()
	if err := cfg.applyEnvOverrides(); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) expandEnv() {
	c.Server.Bind = os.ExpandEnv(c.Server.Bind)
	c.Database.Path = os.ExpandEnv(c.Database.Path)
	c.Auth.AdminToken = os.ExpandEnv(c.Auth.AdminToken)
	c.Auth.BootstrapAdminToken = os.ExpandEnv(c.Auth.BootstrapAdminToken)
	c.Sidecar.Default.BaseURL = os.ExpandEnv(c.Sidecar.Default.BaseURL)
	c.Sidecar.Default.AuthTokenEnv = os.ExpandEnv(c.Sidecar.Default.AuthTokenEnv)
	c.Sidecar.Default.MinVersion = os.ExpandEnv(c.Sidecar.Default.MinVersion)
	c.Producer.WorkdirRoot = os.ExpandEnv(c.Producer.WorkdirRoot)
	c.Producer.SecretsRoot = os.ExpandEnv(c.Producer.SecretsRoot)
	for i := range c.ManualImportRoots {
		c.ManualImportRoots[i] = os.ExpandEnv(c.ManualImportRoots[i])
	}
	for name, tool := range c.Producer.Tools {
		tool.Binary = os.ExpandEnv(tool.Binary)
		for i := range tool.APIArgsAllowlist {
			tool.APIArgsAllowlist[i] = os.ExpandEnv(tool.APIArgsAllowlist[i])
		}
		c.Producer.Tools[name] = tool
	}
	c.EchoOutputDefaults.Kind = os.ExpandEnv(c.EchoOutputDefaults.Kind)
	c.EchoOutputDefaults.BasePath = os.ExpandEnv(c.EchoOutputDefaults.BasePath)
	c.Log.Level = os.ExpandEnv(c.Log.Level)
	c.Log.Format = os.ExpandEnv(c.Log.Format)
	c.SecretsRoot = os.ExpandEnv(c.SecretsRoot)
	c.EmbyProxy.ConfigSync = os.ExpandEnv(c.EmbyProxy.ConfigSync)
	c.EmbyProxy.PublicBaseURL = os.ExpandEnv(c.EmbyProxy.PublicBaseURL)
	c.EmbyProxy.ProxyPrefix = os.ExpandEnv(c.EmbyProxy.ProxyPrefix)
	c.EmbyProxy.Upstream.ID = os.ExpandEnv(c.EmbyProxy.Upstream.ID)
	c.EmbyProxy.Upstream.Name = os.ExpandEnv(c.EmbyProxy.Upstream.Name)
	c.EmbyProxy.Upstream.BaseURL = os.ExpandEnv(c.EmbyProxy.Upstream.BaseURL)
	c.EmbyProxy.Upstream.APIKeyRef = os.ExpandEnv(c.EmbyProxy.Upstream.APIKeyRef)
	for i := range c.EmbyProxy.PathMappings {
		c.EmbyProxy.PathMappings[i].EmbyPathPrefix = os.ExpandEnv(c.EmbyProxy.PathMappings[i].EmbyPathPrefix)
		c.EmbyProxy.PathMappings[i].EchoRelPrefix = os.ExpandEnv(c.EmbyProxy.PathMappings[i].EchoRelPrefix)
	}
}

func (c *Config) applyEnvOverrides() error {
	setString("ECHO_SERVER_BIND", &c.Server.Bind)
	if err := setDuration("ECHO_SERVER_READ_TIMEOUT", &c.Server.ReadTimeout); err != nil {
		return err
	}
	if err := setDuration("ECHO_SERVER_WRITE_TIMEOUT", &c.Server.WriteTimeout); err != nil {
		return err
	}
	setString("ECHO_DATABASE_PATH", &c.Database.Path)
	setString("ECHO_ADMIN_TOKEN", &c.Auth.AdminToken)
	setString("ECHO_BOOTSTRAP_ADMIN_TOKEN", &c.Auth.BootstrapAdminToken)
	setString("ECHO_SIDECAR_DEFAULT_BASE_URL", &c.Sidecar.Default.BaseURL)
	setString("ECHO_SIDECAR_DEFAULT_AUTH_TOKEN_ENV", &c.Sidecar.Default.AuthTokenEnv)
	setString("ECHO_SIDECAR_DEFAULT_MIN_VERSION", &c.Sidecar.Default.MinVersion)
	if err := setDuration("ECHO_SIDECAR_DEFAULT_REQUEST_TIMEOUT", &c.Sidecar.Default.RequestTimeout); err != nil {
		return err
	}
	if err := setDuration("ECHO_SIDECAR_DEFAULT_STREAM_TIMEOUT", &c.Sidecar.Default.StreamTimeout); err != nil {
		return err
	}
	setString("ECHO_PRODUCER_WORKDIR_ROOT", &c.Producer.WorkdirRoot)
	setString("ECHO_PRODUCER_SECRETS_ROOT", &c.Producer.SecretsRoot)
	if err := setDuration("ECHO_PRODUCER_DEFAULT_TIMEOUT", &c.Producer.DefaultTimeout); err != nil {
		return err
	}
	if err := setInt("ECHO_JOBS_MAX_CONCURRENT", &c.Jobs.MaxConcurrent); err != nil {
		return err
	}
	if err := setInt("ECHO_JOBS_WORKER_PER_JOB", &c.Jobs.WorkerPerJob); err != nil {
		return err
	}
	setString("ECHO_ECHO_OUTPUT_DEFAULTS_KIND", &c.EchoOutputDefaults.Kind)
	setString("ECHO_OUTPUT_DEFAULTS_KIND", &c.EchoOutputDefaults.Kind)
	setString("ECHO_ECHO_OUTPUT_DEFAULTS_BASE_PATH", &c.EchoOutputDefaults.BasePath)
	setString("ECHO_OUTPUT_DEFAULTS_BASE_PATH", &c.EchoOutputDefaults.BasePath)
	setString("ECHO_LOG_LEVEL", &c.Log.Level)
	setString("ECHO_LOG_FORMAT", &c.Log.Format)

	if roots, ok := os.LookupEnv("ECHO_MANUAL_IMPORT_ROOTS"); ok {
		if roots == "" {
			c.ManualImportRoots = nil
		} else {
			c.ManualImportRoots = strings.Split(roots, string(os.PathListSeparator))
		}
	}

	if c.Producer.Tools != nil {
		if tool, ok := c.Producer.Tools["115share2cas"]; ok {
			setString("ECHO_PRODUCER_TOOL_115SHARE2CAS_BINARY", &tool.Binary)
			c.Producer.Tools["115share2cas"] = tool
		}
	}

	return nil
}

func (c *Config) validate() error {
	if c.Server.Bind == "" {
		return fieldRequired("server.bind")
	}
	if c.Server.ReadTimeout.Duration <= 0 {
		return fieldPositiveDuration("server.read_timeout")
	}
	if c.Server.WriteTimeout.Duration <= 0 {
		return fieldPositiveDuration("server.write_timeout")
	}
	if c.Database.Path == "" {
		return fieldRequired("database.path")
	}
	if !filepath.IsAbs(c.Database.Path) {
		return fieldAbsolute("database.path", c.Database.Path)
	}
	if c.Auth.BootstrapAdminToken == "" {
		return fieldRequired("auth.bootstrap_admin_token")
	}

	sidecar := c.Sidecar.Default
	if sidecar.BaseURL == "" {
		return fieldRequired("sidecar.default.base_url")
	}
	if parsed, err := url.Parse(sidecar.BaseURL); err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("sidecar.default.base_url must be an absolute URL, got %q", sidecar.BaseURL)
	}
	if sidecar.AuthTokenEnv == "" {
		return fieldRequired("sidecar.default.auth_token_env")
	}
	if sidecar.MinVersion == "" {
		return fieldRequired("sidecar.default.min_version")
	}
	if sidecar.RequestTimeout.Duration <= 0 {
		return fieldPositiveDuration("sidecar.default.request_timeout")
	}
	if sidecar.StreamTimeout.Duration <= 0 {
		return fieldPositiveDuration("sidecar.default.stream_timeout")
	}

	if err := requireExistingAbsDir("producer.workdir_root", c.Producer.WorkdirRoot); err != nil {
		return err
	}
	if err := requireExistingAbsDir("producer.secrets_root", c.Producer.SecretsRoot); err != nil {
		return err
	}
	if c.Producer.DefaultTimeout.Duration <= 0 {
		return fieldPositiveDuration("producer.default_timeout")
	}
	if _, ok := c.Producer.Tools["115share2cas"]; !ok {
		return fieldRequired("producer.tools.115share2cas")
	}
	for name, tool := range c.Producer.Tools {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("producer.tools contains an empty tool name")
		}
		if tool.Binary == "" {
			return fieldRequired(fmt.Sprintf("producer.tools.%s.binary", name))
		}
		if len(tool.APIArgsAllowlist) == 0 {
			return fieldRequired(fmt.Sprintf("producer.tools.%s.api_args_allowlist", name))
		}
	}
	for i, root := range c.ManualImportRoots {
		if err := requireExistingAbsDir(fmt.Sprintf("manual_import_roots[%d]", i), root); err != nil {
			return err
		}
	}
	if c.Jobs.MaxConcurrent <= 0 {
		return fieldPositiveInt("jobs.max_concurrent")
	}
	if c.Jobs.WorkerPerJob <= 0 {
		return fieldPositiveInt("jobs.worker_per_job")
	}
	if c.EchoOutputDefaults.Kind == "" {
		return fieldRequired("echo_output_defaults.kind")
	}
	if c.EchoOutputDefaults.Kind != "local" {
		return fmt.Errorf("echo_output_defaults.kind must be local in v0.1, got %q", c.EchoOutputDefaults.Kind)
	}
	if c.EchoOutputDefaults.BasePath == "" {
		return fieldRequired("echo_output_defaults.base_path")
	}
	if !filepath.IsAbs(c.EchoOutputDefaults.BasePath) {
		return fieldAbsolute("echo_output_defaults.base_path", c.EchoOutputDefaults.BasePath)
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	switch strings.ToLower(c.Log.Level) {
	case "debug", "info", "warn", "warning", "error":
	default:
		return fmt.Errorf("log.level must be debug, info, warn, or error, got %q", c.Log.Level)
	}
	if c.Log.Format == "" {
		c.Log.Format = "json"
	}
	switch strings.ToLower(c.Log.Format) {
	case "json", "text":
	default:
		return fmt.Errorf("log.format must be json or text, got %q", c.Log.Format)
	}
	if err := c.validateEmbyProxy(); err != nil {
		return err
	}
	return nil
}

func (c *Config) validateEmbyProxy() error {
	// config_sync defaults to seed_if_missing and is validated even when the
	// proxy is disabled so an operator typo surfaces immediately.
	if c.EmbyProxy.ConfigSync == "" {
		c.EmbyProxy.ConfigSync = "seed_if_missing"
	}
	switch c.EmbyProxy.ConfigSync {
	case "seed_if_missing", "overwrite_on_startup":
	default:
		return fmt.Errorf("emby_proxy.config_sync must be seed_if_missing or overwrite_on_startup, got %q", c.EmbyProxy.ConfigSync)
	}

	if !c.EmbyProxy.Enabled {
		return nil
	}
	if c.EmbyProxy.Playback.RedactMappedPath == nil {
		redact := true
		c.EmbyProxy.Playback.RedactMappedPath = &redact
	}

	if c.EmbyProxy.PublicBaseURL == "" {
		return fieldRequired("emby_proxy.public_base_url")
	}
	if parsed, err := url.Parse(c.EmbyProxy.PublicBaseURL); err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("emby_proxy.public_base_url must be an absolute URL, got %q", c.EmbyProxy.PublicBaseURL)
	}
	if c.EmbyProxy.ProxyPrefix == "" {
		return fieldRequired("emby_proxy.proxy_prefix")
	}
	if !strings.HasPrefix(c.EmbyProxy.ProxyPrefix, "/") {
		return fmt.Errorf("emby_proxy.proxy_prefix must start with %q, got %q", "/", c.EmbyProxy.ProxyPrefix)
	}
	if c.EmbyProxy.ProxyPrefix != "/" && strings.HasSuffix(c.EmbyProxy.ProxyPrefix, "/") {
		return fmt.Errorf("emby_proxy.proxy_prefix must not end with %q, got %q", "/", c.EmbyProxy.ProxyPrefix)
	}

	up := c.EmbyProxy.Upstream
	if up.ID == "" {
		return fieldRequired("emby_proxy.upstream.id")
	}
	if up.Name == "" {
		return fieldRequired("emby_proxy.upstream.name")
	}
	if up.BaseURL == "" {
		return fieldRequired("emby_proxy.upstream.base_url")
	}
	if parsed, err := url.Parse(up.BaseURL); err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("emby_proxy.upstream.base_url must be an absolute URL, got %q", up.BaseURL)
	}
	if up.APIKeyRef == "" {
		return fieldRequired("emby_proxy.upstream.api_key_ref")
	}
	if err := c.validateAPIKeyRef(up.APIKeyRef); err != nil {
		return err
	}

	if c.EmbyProxy.Playback.SessionTTL.Duration <= 0 {
		return fieldPositiveDuration("emby_proxy.playback.session_ttl")
	}
	if c.EmbyProxy.Playback.MaxCandidateCopies < 1 || c.EmbyProxy.Playback.MaxCandidateCopies > 20 {
		return fmt.Errorf("emby_proxy.playback.max_candidate_copies must be between 1 and 20, got %d", c.EmbyProxy.Playback.MaxCandidateCopies)
	}

	for i, m := range c.EmbyProxy.PathMappings {
		if m.LibraryID <= 0 {
			return fieldPositiveInt(fmt.Sprintf("emby_proxy.path_mappings[%d].library_id", i))
		}
		if m.EmbyPathPrefix == "" {
			return fieldRequired(fmt.Sprintf("emby_proxy.path_mappings[%d].emby_path_prefix", i))
		}
		if filepath.IsAbs(m.EchoRelPrefix) {
			return fmt.Errorf("emby_proxy.path_mappings[%d].echo_rel_prefix must be a relative path, got %q", i, m.EchoRelPrefix)
		}
	}
	return nil
}

// validateAPIKeyRef enforces the api_key_ref contract: either "env:NAME" or
// "ref:relative/path", where the ref path is a regular file located inside
// secrets_root after symlink resolution. Absolute paths, "..", symlink escapes,
// missing files, and non-regular files are rejected.
func (c *Config) validateAPIKeyRef(ref string) error {
	const field = "emby_proxy.upstream.api_key_ref"
	if name, ok := strings.CutPrefix(ref, "env:"); ok {
		if name == "" {
			return fmt.Errorf("%s env reference must name a variable, got %q", field, ref)
		}
		return nil
	}
	rel, ok := strings.CutPrefix(ref, "ref:")
	if !ok {
		return fmt.Errorf("%s must be %q or %q, got %q", field, "env:NAME", "ref:relative/path", ref)
	}
	if rel == "" {
		return fmt.Errorf("%s ref path must not be empty", field)
	}
	if c.SecretsRoot == "" {
		return fmt.Errorf("%s uses a ref path but secrets_root is not configured", field)
	}
	if filepath.IsAbs(rel) {
		return fmt.Errorf("%s ref path must be relative, got %q", field, rel)
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("%s ref path must not traverse outside secrets_root, got %q", field, rel)
	}

	root, err := filepath.Abs(c.SecretsRoot)
	if err != nil {
		return fmt.Errorf("%s: resolve secrets_root: %w", field, err)
	}
	if resolved, rerr := filepath.EvalSymlinks(root); rerr == nil {
		root = resolved
	}
	target := filepath.Join(root, clean)

	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return fmt.Errorf("%s ref path %q must resolve to an existing file: %w", field, rel, err)
	}
	prefix := root + string(os.PathSeparator)
	if resolvedTarget != root && !strings.HasPrefix(resolvedTarget, prefix) {
		return fmt.Errorf("%s ref path %q resolves outside secrets_root", field, rel)
	}
	info, err := os.Stat(resolvedTarget)
	if err != nil {
		return fmt.Errorf("%s ref path %q must be an existing file: %w", field, rel, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s ref path %q must be a regular file", field, rel)
	}
	return nil
}

func setString(env string, dst *string) {
	if value, ok := os.LookupEnv(env); ok {
		*dst = value
	}
}

func setDuration(env string, dst *Duration) error {
	value, ok := os.LookupEnv(env)
	if !ok {
		return nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("%s must be a duration: %w", env, err)
	}
	dst.Duration = parsed
	return nil
}

func setInt(env string, dst *int) error {
	value, ok := os.LookupEnv(env)
	if !ok {
		return nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("%s must be an integer: %w", env, err)
	}
	*dst = parsed
	return nil
}

func requireExistingAbsDir(name, path string) error {
	if path == "" {
		return fieldRequired(name)
	}
	if !filepath.IsAbs(path) {
		return fieldAbsolute(name, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s must be an existing directory: %w", name, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s must be a directory, got %q", name, path)
	}
	return nil
}

func fieldRequired(name string) error {
	return fmt.Errorf("%s is required", name)
}

func fieldPositiveDuration(name string) error {
	return fmt.Errorf("%s must be a positive duration", name)
}

func fieldPositiveInt(name string) error {
	return fmt.Errorf("%s must be a positive integer", name)
}

func fieldAbsolute(name, value string) error {
	return fmt.Errorf("%s must be an absolute path, got %q", name, value)
}
