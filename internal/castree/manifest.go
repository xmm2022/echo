package castree

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type Item struct {
	RelPath    string
	Name       string
	Size       int64
	SHA1       string
	PreID      string
	MD5        string
	SliceMD5   string
	SHA256     string
	CreateTime string
	Provider   string
}

type ParseError struct {
	Line int
	Err  error
}

func (e ParseError) Error() string {
	return fmt.Sprintf("manifest line %d: %v", e.Line, e.Err)
}

func (e ParseError) Unwrap() error {
	return e.Err
}

type Manifest struct {
	scanner   *bufio.Scanner
	line      int
	parseErrs []ParseError
	scanErr   error
}

type Reader = Manifest

func NewManifest(r io.Reader) *Manifest {
	scanner := bufio.NewScanner(r)
	const maxLineLen = 1024 * 1024
	scanner.Buffer(make([]byte, 64*1024), maxLineLen)
	return &Manifest{scanner: scanner}
}

func NewReader(r io.Reader) *Reader {
	return NewManifest(r)
}

func (m *Manifest) Next() (Item, bool) {
	for m.scanner.Scan() {
		m.line++
		line := strings.TrimSpace(m.scanner.Text())
		if line == "" {
			continue
		}

		item, err := parseManifestLine([]byte(line))
		if err != nil {
			m.parseErrs = append(m.parseErrs, ParseError{Line: m.line, Err: err})
			continue
		}
		return item, true
	}

	if err := m.scanner.Err(); err != nil && m.scanErr == nil {
		m.scanErr = err
		m.parseErrs = append(m.parseErrs, ParseError{Line: m.line + 1, Err: err})
	}
	return Item{}, false
}

func (m *Manifest) ParseErrors() []ParseError {
	out := make([]ParseError, len(m.parseErrs))
	copy(out, m.parseErrs)
	return out
}

func (m *Manifest) Err() error {
	return m.scanErr
}

func ReadManifest(r io.Reader) ([]Item, []ParseError, error) {
	m := NewManifest(r)
	items := make([]Item, 0)
	for {
		item, ok := m.Next()
		if !ok {
			break
		}
		items = append(items, item)
	}
	return items, m.ParseErrors(), m.Err()
}

type manifestRecord struct {
	RelPath    string `json:"rel_path"`
	Path       string `json:"path"`
	Name       string `json:"name"`
	Size       *int64 `json:"size"`
	SHA1       string `json:"sha1"`
	PreID      string `json:"preID"`
	MD5        string `json:"md5"`
	SliceMD5   string `json:"sliceMd5"`
	SHA256     string `json:"sha256"`
	CreateTime string `json:"create_time"`
	Provider   string `json:"provider"`
}

func parseManifestLine(line []byte) (Item, error) {
	var record manifestRecord
	if err := json.Unmarshal(line, &record); err != nil {
		return Item{}, fmt.Errorf("decode manifest json: %w", err)
	}

	relPath := record.RelPath
	if relPath == "" {
		relPath = record.Path
	}
	item := Item{
		RelPath:    relPath,
		Name:       record.Name,
		SHA1:       strings.TrimSpace(record.SHA1),
		PreID:      strings.TrimSpace(record.PreID),
		MD5:        strings.TrimSpace(record.MD5),
		SliceMD5:   strings.TrimSpace(record.SliceMD5),
		SHA256:     strings.TrimSpace(record.SHA256),
		CreateTime: strings.TrimSpace(record.CreateTime),
		Provider:   strings.TrimSpace(record.Provider),
	}
	if item.RelPath == "" {
		return Item{}, fmt.Errorf("missing required field rel_path")
	}
	if item.Name == "" {
		return Item{}, fmt.Errorf("missing required field name")
	}
	if record.Size == nil {
		return Item{}, fmt.Errorf("missing required field size")
	}
	item.Size = *record.Size
	if item.Size < 0 {
		return Item{}, fmt.Errorf("invalid required field size")
	}
	if item.SHA1 == "" && item.PreID == "" && item.MD5 == "" && item.SliceMD5 == "" && item.SHA256 == "" {
		return Item{}, fmt.Errorf("missing required hash field")
	}
	return item, nil
}
