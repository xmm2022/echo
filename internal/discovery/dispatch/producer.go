package dispatch

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/xmm2022/echo/internal/ingest"
	"github.com/xmm2022/echo/internal/job"
	"github.com/xmm2022/echo/internal/pathsafe"
)

const (
	provider115      = "115"
	tool115Share2CAS = "115share2cas"
	argShareURL      = "share_url"
	argShareCode     = "share_code"
	argReceiveCode   = "receive_code"
)

type ProducerProfile struct {
	LibraryID            int64
	Provider             string
	Tool                 string
	TargetAccount        string
	TargetSubdirTemplate string
	// Reserved for a future dispatch payload field; job.IngestPayload does not
	// currently carry a library-relative path template.
	LibraryRelPathTemplate string
	DefaultArgs            map[string]any
}

type Resource struct {
	ShareURL    string
	ShareCode   string
	ReceiveCode string
	Title       string
	Year        int
}

func BuildProducerPayload(profile ProducerProfile, res Resource) (job.IngestPayload, error) {
	if profile.Provider != provider115 {
		return job.IngestPayload{}, fmt.Errorf("unsupported producer provider %q", profile.Provider)
	}
	if profile.Tool != tool115Share2CAS {
		return job.IngestPayload{}, fmt.Errorf("unsupported producer tool %q", profile.Tool)
	}

	args, err := copyDefaultArgs(profile.DefaultArgs)
	if err != nil {
		return job.IngestPayload{}, err
	}
	shareCode := strings.TrimSpace(res.ShareCode)
	receiveCode := strings.TrimSpace(res.ReceiveCode)
	if shareCode != "" && receiveCode != "" {
		args[argShareCode] = shareCode
		args[argReceiveCode] = receiveCode
	} else if shareURL := strings.TrimSpace(res.ShareURL); shareURL != "" {
		args[argShareURL] = shareURL
	} else {
		return job.IngestPayload{}, fmt.Errorf("missing 115 share source")
	}

	targetSubdir, err := renderTemplate("target_subdir", profile.TargetSubdirTemplate, res)
	if err != nil {
		return job.IngestPayload{}, err
	}
	targetSubdir, err = normalizeTargetSubdir(targetSubdir)
	if err != nil {
		return job.IngestPayload{}, err
	}
	return job.IngestPayload{
		LibraryID:     profile.LibraryID,
		TargetAccount: profile.TargetAccount,
		TargetSubdir:  targetSubdir,
		Tool:          profile.Tool,
		Args:          args,
	}, nil
}

func ValidatePayload(payload job.IngestPayload, cfg ingest.ProducerConfig) error {
	return ingest.ValidateProducerRequest(payload.Tool, payload.Args, cfg)
}

func copyDefaultArgs(defaultArgs map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(defaultArgs)+2)
	for key, value := range defaultArgs {
		switch key {
		case argShareURL, argShareCode, argReceiveCode:
			return nil, fmt.Errorf("profile default arg %q is not allowed", key)
		default:
			out[key] = value
		}
	}
	return out, nil
}

func normalizeTargetSubdir(targetSubdir string) (string, error) {
	if err := pathsafe.ValidateRelPath(targetSubdir); err != nil {
		return "", fmt.Errorf("invalid target_subdir: %w", err)
	}
	return filepath.ToSlash(filepath.Clean(targetSubdir)), nil
}

func renderTemplate(name, tmpl string, data Resource) (string, error) {
	if tmpl == "" {
		return "", nil
	}
	parsed, err := template.New(name).Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("parse %s template: %w", name, err)
	}
	var buf bytes.Buffer
	if err := parsed.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render %s template: %w", name, err)
	}
	return buf.String(), nil
}
