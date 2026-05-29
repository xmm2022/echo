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
	AdminToken string `yaml:"admin_token"`
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
	if c.Auth.AdminToken == "" {
		return fieldRequired("auth.admin_token")
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
