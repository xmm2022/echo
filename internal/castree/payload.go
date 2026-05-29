package castree

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// Payload mirrors the upstream openlist-guangyapan-src feat/cas-tools
// pkg/casmeta field table; Echo reimplements the codec instead of importing
// sidecar code. See docs/superpowers/specs §10.
type Payload struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	Provider   string `json:"provider,omitempty"`
	SHA1       string `json:"sha1,omitempty"`
	PreID      string `json:"preID,omitempty"`
	MD5        string `json:"md5"`
	SliceMD5   string `json:"sliceMd5"`
	SHA256     string `json:"sha256,omitempty"`
	CreateTime string `json:"create_time"`
}

func Encode(p Payload) ([]byte, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	out := make([]byte, base64.StdEncoding.EncodedLen(len(body)))
	base64.StdEncoding.Encode(out, body)
	return out, nil
}

func Decode(data []byte) (Payload, error) {
	data = bytes.TrimSpace(data)
	decoded := make([]byte, base64.StdEncoding.DecodedLen(len(data)))
	n, err := base64.StdEncoding.Decode(decoded, data)
	if err != nil {
		return Payload{}, fmt.Errorf("decode cas payload base64: %w", err)
	}

	var payload Payload
	if err := json.Unmarshal(decoded[:n], &payload); err != nil {
		return Payload{}, fmt.Errorf("decode cas payload json: %w", err)
	}
	return payload, nil
}
