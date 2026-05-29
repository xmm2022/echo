package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	ingestexec "github.com/xmm2022/echo/internal/ingest/exec"
	"github.com/xmm2022/echo/internal/pathsafe"
	"github.com/xmm2022/echo/internal/store/queries"
)

const (
	producerTool115Share2CAS = "115share2cas"
	producerDefaultMode      = "transfer-batch"
	producerDirectMode       = "direct"
)

var allowedProducerTools = [...]string{producerTool115Share2CAS}
var _ [1]string = allowedProducerTools

type producerExecRunner interface {
	Run(context.Context, ingestexec.Command) (ingestexec.Result, error)
}

type producerRunSpec struct {
	Tool         string
	Binary       string
	Args         []string
	Argv         []string
	Workdir      string
	OutputDir    string
	ManifestPath string
	StdoutPath   string
	StderrPath   string
	Timeout      time.Duration
}

func RunProducer(ctx context.Context, job Job, deps Deps) error {
	if deps.Store == nil {
		return fmt.Errorf("ingest store is nil")
	}
	if deps.Sidecar == nil {
		return fmt.Errorf("ingest sidecar is nil")
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	runner := deps.ExecRunner
	if runner == nil {
		runner = ingestexec.OSRunner{}
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}

	spec, err := prepareProducerRun(job, deps.Config.Producer)
	if err != nil {
		// Record rejected requests only for known tools so the {tool} label stays
		// bounded (an unknown tool name must not become a new metric series).
		if errors.Is(err, ErrProducerUnauthorized) && isAllowedProducerTool(job.Tool) {
			deps.Metrics.IncProducerRun(job.Tool, "unauthorized")
		}
		return err
	}

	stdoutFile, err := os.OpenFile(spec.StdoutPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open producer stdout log: %w", err)
	}
	defer stdoutFile.Close()

	stderrFile, err := os.OpenFile(spec.StderrPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open producer stderr log: %w", err)
	}
	defer stderrFile.Close()

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return fmt.Errorf("open producer stdin: %w", err)
	}
	defer devNull.Close()

	cmdline, err := marshalProducerArgv(RedactProducerArgv(spec.Argv))
	if err != nil {
		return err
	}
	run, err := deps.Store.CreateProducerRun(ctx, queries.CreateProducerRunParams{
		JobID:        job.ID,
		Tool:         spec.Tool,
		ToolVersion:  producerToolVersion(spec.Binary),
		Cmdline:      cmdline,
		Workdir:      spec.Workdir,
		OutputDir:    spec.OutputDir,
		ManifestPath: sql.NullString{String: spec.ManifestPath, Valid: true},
		StdoutPath:   sql.NullString{String: spec.StdoutPath, Valid: true},
		StderrPath:   sql.NullString{String: spec.StderrPath, Valid: true},
		StartedAt:    now().Unix(),
	})
	if err != nil {
		return fmt.Errorf("create producer run: %w", err)
	}
	logger.Info("producer run started", "tool", spec.Tool, "job_id", job.ID, "producer_run_id", run.ID)

	result, runErr := runner.Run(ctx, ingestexec.Command{
		Path:    spec.Binary,
		Args:    spec.Args,
		Dir:     spec.Workdir,
		Env:     producerEnv(),
		Stdin:   devNull,
		Stdout:  stdoutFile,
		Stderr:  stderrFile,
		Timeout: spec.Timeout,
	})
	exitCode := result.ExitCode
	if runErr != nil && exitCode == 0 {
		exitCode = -1
	}
	finishCtx := context.WithoutCancel(ctx)
	finishErr := deps.Store.FinishProducerRun(finishCtx, queries.FinishProducerRunParams{
		ID:           run.ID,
		ExitCode:     sql.NullInt64{Int64: int64(exitCode), Valid: true},
		FinishedAt:   sql.NullInt64{Int64: now().Unix(), Valid: true},
		ManifestPath: sql.NullString{String: spec.ManifestPath, Valid: true},
		StdoutPath:   sql.NullString{String: spec.StdoutPath, Valid: true},
		StderrPath:   sql.NullString{String: spec.StderrPath, Valid: true},
	})
	if finishErr != nil {
		return fmt.Errorf("finish producer run: %w", finishErr)
	}
	logger.Info("producer run finished",
		"tool", spec.Tool, "job_id", job.ID, "producer_run_id", run.ID,
		"exit_code", exitCode, "timed_out", result.TimedOut)
	if runErr != nil || exitCode != 0 {
		if result.TimedOut {
			deps.Metrics.IncProducerRun(spec.Tool, "timeout")
			return fmt.Errorf("%w: timed out after %s", ErrProducerExitFailed, spec.Timeout)
		}
		deps.Metrics.IncProducerRun(spec.Tool, "exit_failed")
		if runErr != nil {
			return fmt.Errorf("%w: %v", ErrProducerExitFailed, runErr)
		}
		return fmt.Errorf("%w: exit code %d", ErrProducerExitFailed, exitCode)
	}
	deps.Metrics.IncProducerRun(spec.Tool, "success")

	outputDir, manifestPath, err := validateProducerOutputs(spec.Workdir)
	if err != nil {
		return err
	}
	manualJob := job
	manualJob.CASTreePath = outputDir
	manualJob.ManifestPath = manifestPath
	return RunManual(ctx, manualJob, deps)
}

func prepareProducerRun(job Job, cfg ProducerConfig) (producerRunSpec, error) {
	if job.ID <= 0 {
		return producerRunSpec{}, fmt.Errorf("%w: job id must be positive", ErrProducerUnauthorized)
	}
	if !isAllowedProducerTool(job.Tool) {
		return producerRunSpec{}, fmt.Errorf("%w: tool %q", ErrProducerUnauthorized, job.Tool)
	}
	toolCfg, ok := cfg.Tools[job.Tool]
	if !ok {
		return producerRunSpec{}, fmt.Errorf("%w: tool %q is not configured", ErrProducerUnauthorized, job.Tool)
	}
	if toolCfg.Binary == "" {
		return producerRunSpec{}, fmt.Errorf("%w: tool %q binary is empty", ErrProducerUnauthorized, job.Tool)
	}
	allowlist := makeAllowlist(toolCfg.APIArgsAllowlist)
	for key := range job.Args {
		if !allowlist[key] || !producerArgSpecsByKey[key].Known {
			return producerRunSpec{}, fmt.Errorf("%w: arg %q", ErrProducerUnauthorized, key)
		}
	}
	if err := validateProducerArgTypes(job.Args); err != nil {
		return producerRunSpec{}, err
	}
	if err := validateProducerArgCombination(job.Args); err != nil {
		return producerRunSpec{}, err
	}

	resolvedArgs, err := resolveProducerRefs(job.Args, cfg.SecretsRoot)
	if err != nil {
		return producerRunSpec{}, err
	}
	workdir, err := pathsafe.PrepareNewDirUnderRoot(cfg.WorkdirRoot, fmt.Sprintf("job-%d", job.ID))
	if err != nil {
		return producerRunSpec{}, fmt.Errorf("%w: prepare producer workdir: %v", ErrProducerUnauthorized, err)
	}
	outputDir := filepath.Join(workdir, "cas")
	if err := os.Mkdir(outputDir, 0o750); err != nil {
		return producerRunSpec{}, fmt.Errorf("create producer output dir: %w", err)
	}
	manifestPath := filepath.Join(workdir, "manifest.jsonl")
	stdoutPath := filepath.Join(workdir, "stdout.log")
	stderrPath := filepath.Join(workdir, "stderr.log")

	args, err := buildProducerArgs(resolvedArgs, outputDir, manifestPath)
	if err != nil {
		return producerRunSpec{}, err
	}
	argv := append([]string{toolCfg.Binary}, args...)
	return producerRunSpec{
		Tool:         job.Tool,
		Binary:       toolCfg.Binary,
		Args:         args,
		Argv:         argv,
		Workdir:      workdir,
		OutputDir:    outputDir,
		ManifestPath: manifestPath,
		StdoutPath:   stdoutPath,
		StderrPath:   stderrPath,
		Timeout:      cfg.DefaultTimeout,
	}, nil
}

func isAllowedProducerTool(tool string) bool {
	for _, allowed := range allowedProducerTools {
		if tool == allowed {
			return true
		}
	}
	return false
}

// ValidateProducerRequest runs the producer pre-flight checks an HTTP handler can
// make synchronously before enqueuing a job: the tool is allowed and configured,
// every arg is allowlisted and known, arg types and the share-source combination
// are valid, and secret args use the ref: form. It performs NO filesystem access —
// workdir creation and secret-file resolution happen later in RunProducer, so a
// ref: to a not-yet-present file still passes here.
func ValidateProducerRequest(tool string, args map[string]any, cfg ProducerConfig) error {
	if !isAllowedProducerTool(tool) {
		return fmt.Errorf("%w: tool %q", ErrProducerUnauthorized, tool)
	}
	toolCfg, ok := cfg.Tools[tool]
	if !ok {
		return fmt.Errorf("%w: tool %q is not configured", ErrProducerUnauthorized, tool)
	}
	if toolCfg.Binary == "" {
		return fmt.Errorf("%w: tool %q binary is empty", ErrProducerUnauthorized, tool)
	}
	allowlist := makeAllowlist(toolCfg.APIArgsAllowlist)
	for key := range args {
		if !allowlist[key] || !producerArgSpecsByKey[key].Known {
			return fmt.Errorf("%w: arg %q", ErrProducerUnauthorized, key)
		}
	}
	if err := validateProducerArgTypes(args); err != nil {
		return err
	}
	if err := validateProducerArgCombination(args); err != nil {
		return err
	}
	return validateProducerRefForms(args)
}

// validateProducerRefForms checks the ref: shape of every secret arg without
// touching the filesystem.
func validateProducerRefForms(args map[string]any) error {
	for _, key := range []string{"cookie_file", "recycle_password_file"} {
		if !hasNonEmptyArg(args, key) {
			continue
		}
		ref, err := stringArg(args, key)
		if err != nil {
			return err
		}
		if _, err := producerRefRelPath(ref); err != nil {
			return err
		}
	}
	return nil
}

// producerRefRelPath validates the ref: form (prefix present, non-empty, relative)
// and returns the relative path. It does not touch the filesystem.
func producerRefRelPath(ref string) (string, error) {
	const prefix = "ref:"
	if !strings.HasPrefix(ref, prefix) {
		return "", fmt.Errorf("%w: secret args must use ref:", ErrProducerUnauthorized)
	}
	rel := strings.TrimPrefix(ref, prefix)
	if rel == "" {
		return "", fmt.Errorf("%w: empty secret ref", ErrProducerUnauthorized)
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: secret ref must be relative", ErrProducerUnauthorized)
	}
	return rel, nil
}

type producerArgSpec struct {
	Known bool
	Flag  string
	Kind  producerArgKind
}

type producerArgKind int

const (
	producerArgString producerArgKind = iota
	producerArgBool
	producerArgInt
)

var producerArgOrder = []string{
	"share_url",
	"share_code",
	"receive_code",
	"cookie_file",
	"mode",
	"batch_size",
	"temp_parent_cid",
	"recycle_password_file",
	"keep_temp",
	"limit",
}

var producerArgSpecsByKey = map[string]producerArgSpec{
	"share_url":             {Known: true, Flag: "--share-url", Kind: producerArgString},
	"share_code":            {Known: true, Flag: "--share-code", Kind: producerArgString},
	"receive_code":          {Known: true, Flag: "--receive-code", Kind: producerArgString},
	"cookie_file":           {Known: true, Flag: "--cookie-file", Kind: producerArgString},
	"mode":                  {Known: true, Flag: "--mode", Kind: producerArgString},
	"batch_size":            {Known: true, Flag: "--batch-size", Kind: producerArgString},
	"temp_parent_cid":       {Known: true, Flag: "--temp-parent-cid", Kind: producerArgString},
	"recycle_password_file": {Known: true, Flag: "--recycle-password-file", Kind: producerArgString},
	"keep_temp":             {Known: true, Flag: "--keep-temp", Kind: producerArgBool},
	"limit":                 {Known: true, Flag: "--limit", Kind: producerArgInt},
}

func makeAllowlist(keys []string) map[string]bool {
	out := make(map[string]bool, len(keys))
	for _, key := range keys {
		out[key] = true
	}
	return out
}

func validateProducerArgCombination(args map[string]any) error {
	hasShareURL := hasNonEmptyArg(args, "share_url")
	hasShareCode := hasNonEmptyArg(args, "share_code")
	hasReceiveCode := hasNonEmptyArg(args, "receive_code")
	if hasShareURL && hasShareCode {
		return fmt.Errorf("%w: share_url and share_code are mutually exclusive", ErrProducerUnauthorized)
	}
	if !hasShareURL && !(hasShareCode && hasReceiveCode) {
		return fmt.Errorf("%w: share_url or share_code plus receive_code is required", ErrProducerUnauthorized)
	}

	mode := producerDefaultMode
	if hasNonEmptyArg(args, "mode") {
		value, err := stringArg(args, "mode")
		if err != nil {
			return err
		}
		mode = value
	}
	switch mode {
	case producerDefaultMode, producerDirectMode:
	default:
		return fmt.Errorf("%w: unsupported mode %q", ErrProducerUnauthorized, mode)
	}
	if mode != producerDefaultMode {
		return nil
	}
	if !hasNonEmptyArg(args, "cookie_file") {
		return fmt.Errorf("%w: cookie_file is required for transfer-batch", ErrProducerUnauthorized)
	}
	keepTemp, err := boolArgDefault(args, "keep_temp", false)
	if err != nil {
		return err
	}
	if !keepTemp && !hasNonEmptyArg(args, "recycle_password_file") {
		return fmt.Errorf("%w: recycle_password_file is required for transfer-batch without keep_temp", ErrProducerUnauthorized)
	}
	return nil
}

func resolveProducerRefs(args map[string]any, secretsRoot string) (map[string]any, error) {
	out := make(map[string]any, len(args))
	for key, value := range args {
		out[key] = value
	}
	for _, key := range []string{"cookie_file", "recycle_password_file"} {
		if !hasNonEmptyArg(args, key) {
			continue
		}
		ref, err := stringArg(args, key)
		if err != nil {
			return nil, err
		}
		resolved, err := resolveProducerRef(secretsRoot, ref)
		if err != nil {
			return nil, err
		}
		out[key] = resolved
	}
	return out, nil
}

func resolveProducerRef(secretsRoot, ref string) (string, error) {
	rel, err := producerRefRelPath(ref)
	if err != nil {
		return "", err
	}
	final, err := pathsafe.ResolveExistingUnderRoot(secretsRoot, rel)
	if err != nil {
		return "", fmt.Errorf("%w: resolve secret ref: %v", ErrProducerUnauthorized, err)
	}
	info, err := os.Lstat(final)
	if err != nil {
		return "", fmt.Errorf("%w: stat secret ref: %v", ErrProducerUnauthorized, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: secret ref is not a regular file", ErrProducerUnauthorized)
	}
	return final, nil
}

func validateProducerArgTypes(args map[string]any) error {
	for _, key := range producerArgOrder {
		value, ok := args[key]
		if !ok {
			continue
		}
		spec := producerArgSpecsByKey[key]
		switch spec.Kind {
		case producerArgBool:
			if _, err := boolArgValue(key, value); err != nil {
				return err
			}
		case producerArgInt:
			if _, err := intArgValue(key, value); err != nil {
				return err
			}
		default:
			if _, err := stringArgValue(key, value); err != nil {
				return err
			}
		}
	}
	return nil
}

func buildProducerArgs(args map[string]any, outputDir, manifestPath string) ([]string, error) {
	var argv []string
	for _, key := range producerArgOrder {
		value, ok := args[key]
		if !ok {
			continue
		}
		spec := producerArgSpecsByKey[key]
		switch spec.Kind {
		case producerArgBool:
			enabled, err := boolArgValue(key, value)
			if err != nil {
				return nil, err
			}
			if enabled {
				argv = append(argv, spec.Flag)
			}
		case producerArgInt:
			encoded, err := intArgValue(key, value)
			if err != nil {
				return nil, err
			}
			argv = append(argv, spec.Flag, encoded)
		default:
			encoded, err := stringArgValue(key, value)
			if err != nil {
				return nil, err
			}
			if encoded == "" {
				continue
			}
			argv = append(argv, spec.Flag, encoded)
		}
	}
	argv = append(argv, "--out", outputDir, "--manifest", manifestPath)
	return argv, nil
}

func hasNonEmptyArg(args map[string]any, key string) bool {
	value, ok := args[key]
	if !ok {
		return false
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v) != ""
	default:
		return value != nil
	}
}

func stringArg(args map[string]any, key string) (string, error) {
	value, ok := args[key]
	if !ok {
		return "", fmt.Errorf("%w: missing arg %q", ErrProducerUnauthorized, key)
	}
	return stringArgValue(key, value)
}

func stringArgValue(key string, value any) (string, error) {
	v, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%w: arg %q must be a string", ErrProducerUnauthorized, key)
	}
	return v, nil
}

func boolArgDefault(args map[string]any, key string, fallback bool) (bool, error) {
	value, ok := args[key]
	if !ok {
		return fallback, nil
	}
	return boolArgValue(key, value)
}

func boolArgValue(key string, value any) (bool, error) {
	switch v := value.(type) {
	case bool:
		return v, nil
	case string:
		parsed, err := strconv.ParseBool(v)
		if err != nil {
			return false, fmt.Errorf("%w: arg %q must be a bool", ErrProducerUnauthorized, key)
		}
		return parsed, nil
	default:
		return false, fmt.Errorf("%w: arg %q must be a bool", ErrProducerUnauthorized, key)
	}
}

func intArgValue(key string, value any) (string, error) {
	switch v := value.(type) {
	case int:
		if v < 0 {
			return "", fmt.Errorf("%w: arg %q must be non-negative", ErrProducerUnauthorized, key)
		}
		return strconv.Itoa(v), nil
	case int64:
		if v < 0 {
			return "", fmt.Errorf("%w: arg %q must be non-negative", ErrProducerUnauthorized, key)
		}
		return strconv.FormatInt(v, 10), nil
	case float64:
		if v < 0 || v != float64(int64(v)) {
			return "", fmt.Errorf("%w: arg %q must be a non-negative integer", ErrProducerUnauthorized, key)
		}
		return strconv.FormatInt(int64(v), 10), nil
	case json.Number:
		parsed, err := strconv.ParseInt(string(v), 10, 64)
		if err != nil || parsed < 0 {
			return "", fmt.Errorf("%w: arg %q must be a non-negative integer", ErrProducerUnauthorized, key)
		}
		return strconv.FormatInt(parsed, 10), nil
	case string:
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil || parsed < 0 {
			return "", fmt.Errorf("%w: arg %q must be a non-negative integer", ErrProducerUnauthorized, key)
		}
		return strconv.FormatInt(parsed, 10), nil
	default:
		return "", fmt.Errorf("%w: arg %q must be a non-negative integer", ErrProducerUnauthorized, key)
	}
}

// RedactArgs returns a copy of a producer args map with credential-bearing values
// masked, for safe display in API responses and logs (spec §6/§8: secrets must
// not appear). It mirrors RedactProducerArgv's policy: share_url query secrets and
// receive_code are masked, and the cookie_file / recycle_password_file references
// are replaced with a placeholder. It does not mutate the input.
func RedactArgs(args map[string]any) map[string]any {
	if args == nil {
		return nil
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}
	if s, ok := out["share_url"].(string); ok {
		out["share_url"] = redactProducerURLArg(s)
	}
	if _, ok := out["receive_code"]; ok {
		out["receive_code"] = "<redacted>"
	}
	for _, k := range []string{"cookie_file", "recycle_password_file"} {
		if _, ok := out[k]; ok {
			out[k] = "<redacted-secret-path>"
		}
	}
	return out
}

func RedactProducerArgv(argv []string) []string {
	out := make([]string, len(argv))
	copy(out, argv)
	for i := range out {
		out[i] = redactProducerURLArg(out[i])
	}
	for i := 0; i < len(out); i++ {
		switch out[i] {
		case "--cookie-file", "--recycle-password-file":
			if i+1 < len(out) {
				out[i+1] = "<redacted-secret-path>"
				i++
			}
		case "--receive-code":
			if i+1 < len(out) {
				out[i+1] = "<redacted>"
				i++
			}
		}
	}
	return out
}

func redactProducerURLArg(arg string) string {
	queryAt := strings.IndexByte(arg, '?')
	if queryAt < 0 {
		return arg
	}
	fragmentAt := strings.IndexByte(arg[queryAt+1:], '#')
	if fragmentAt >= 0 {
		fragmentAt += queryAt + 1
	} else {
		fragmentAt = len(arg)
	}

	query := arg[queryAt+1 : fragmentAt]
	parts := strings.Split(query, "&")
	changed := false
	for i, part := range parts {
		key, _, hasValue := strings.Cut(part, "=")
		decodedKey, err := url.QueryUnescape(key)
		if err != nil {
			decodedKey = key
		}
		switch strings.ToLower(decodedKey) {
		case "password", "sign", "token", "signature", "receive_code":
			if hasValue {
				parts[i] = key + "=<redacted>"
			} else {
				parts[i] = key
			}
			changed = true
		}
	}
	if !changed {
		return arg
	}
	return arg[:queryAt+1] + strings.Join(parts, "&") + arg[fragmentAt:]
}

func marshalProducerArgv(argv []string) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(argv); err != nil {
		return "", fmt.Errorf("marshal producer cmdline: %w", err)
	}
	return strings.TrimSpace(buf.String()), nil
}

func producerToolVersion(binary string) sql.NullString {
	f, err := os.Open(binary)
	if err != nil {
		return sql.NullString{String: "binary_sha256:unavailable", Valid: true}
	}
	defer f.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return sql.NullString{String: "binary_sha256:unavailable", Valid: true}
	}
	return sql.NullString{String: "binary_sha256:" + hex.EncodeToString(hash.Sum(nil)), Valid: true}
}

func validateProducerOutputs(workdir string) (string, string, error) {
	info, err := os.Lstat(workdir)
	if err != nil {
		return "", "", fmt.Errorf("lstat producer workdir: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", "", fmt.Errorf("%w: producer workdir is not a directory", ErrProducerUnauthorized)
	}

	outputDir, err := pathsafe.ResolveExistingUnderRoot(workdir, "cas")
	if err != nil {
		return "", "", fmt.Errorf("%w: resolve producer output dir: %v", ErrProducerUnauthorized, err)
	}
	outputInfo, err := os.Lstat(outputDir)
	if err != nil {
		return "", "", fmt.Errorf("%w: stat producer output dir: %v", ErrProducerUnauthorized, err)
	}
	if !outputInfo.IsDir() {
		return "", "", fmt.Errorf("%w: producer output path is not a directory", ErrProducerUnauthorized)
	}

	manifestPath, err := pathsafe.ResolveExistingUnderRoot(workdir, "manifest.jsonl")
	if err != nil {
		return "", "", fmt.Errorf("%w: resolve producer manifest: %v", ErrProducerUnauthorized, err)
	}
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil {
		return "", "", fmt.Errorf("%w: stat producer manifest: %v", ErrProducerUnauthorized, err)
	}
	if !manifestInfo.Mode().IsRegular() {
		return "", "", fmt.Errorf("%w: producer manifest is not a regular file", ErrProducerUnauthorized)
	}
	return outputDir, manifestPath, nil
}

func producerEnv() []string {
	var env []string
	for _, key := range []string{"PATH", "HOME", "LANG"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}
