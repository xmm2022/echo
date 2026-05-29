package castree

import (
	"strings"
	"testing"
)

func TestReadManifestParsesValidRecordsAndSkipsBadLines(t *testing.T) {
	body := strings.Join([]string{
		`{"path":"Season 1/E01.mkv","name":"E01.mkv","size":100,"sha1":"AAAA","preID":"BBBB","file_id":"f1","cas_path":"/tmp/E01.mkv.cas","status":"ok"}`,
		`not-json`,
		`{"rel_path":"Season 1/E02.mkv","name":"E02.mkv","size":200,"md5":"cccc","sliceMd5":"dddd","provider":"189pc","create_time":"1710000002"}`,
		`{"rel_path":"missing-name.mkv","size":1,"sha256":"eeee"}`,
		`{"rel_path":"no-hash.mkv","name":"no-hash.mkv","size":1}`,
	}, "\n")

	items, parseErrs, err := ReadManifest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("ReadManifest() error = %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("item count = %d, want 2: %#v", len(items), items)
	}
	if items[0].RelPath != "Season 1/E01.mkv" || items[0].Name != "E01.mkv" || items[0].SHA1 != "AAAA" || items[0].PreID != "BBBB" {
		t.Fatalf("first item = %#v", items[0])
	}
	if items[1].RelPath != "Season 1/E02.mkv" || items[1].Provider != "189pc" || items[1].MD5 != "cccc" || items[1].SliceMD5 != "dddd" {
		t.Fatalf("second item = %#v", items[1])
	}
	if len(parseErrs) != 3 {
		t.Fatalf("parse error count = %d, want 3: %#v", len(parseErrs), parseErrs)
	}
	if parseErrs[0].Line != 2 || parseErrs[1].Line != 4 || parseErrs[2].Line != 5 {
		t.Fatalf("parse error lines = %#v", parseErrs)
	}
}

func TestManifestReaderStreamsItems(t *testing.T) {
	reader := NewReader(strings.NewReader(strings.Join([]string{
		`{"rel_path":"a.mkv","name":"a.mkv","size":1,"sha256":"` + strings.Repeat("a", 64) + `"}`,
		`{"rel_path":"","name":"bad.mkv","size":1,"sha256":"` + strings.Repeat("b", 64) + `"}`,
		`{"rel_path":"b.mkv","name":"b.mkv","size":2,"sha256":"` + strings.Repeat("c", 64) + `"}`,
	}, "\n")))

	first, ok := reader.Next()
	if !ok {
		t.Fatal("first Next() returned false")
	}
	if first.RelPath != "a.mkv" {
		t.Fatalf("first RelPath = %q", first.RelPath)
	}
	second, ok := reader.Next()
	if !ok {
		t.Fatal("second Next() returned false")
	}
	if second.RelPath != "b.mkv" {
		t.Fatalf("second RelPath = %q", second.RelPath)
	}
	if _, ok := reader.Next(); ok {
		t.Fatal("third Next() returned true")
	}
	if len(reader.ParseErrors()) != 1 {
		t.Fatalf("parse error count = %d, want 1", len(reader.ParseErrors()))
	}
}

func TestReadManifestAcceptsZeroByteFileAndRejectsMissingSize(t *testing.T) {
	body := strings.Join([]string{
		`{"rel_path":"empty.txt","name":"empty.txt","size":0,"sha256":"` + strings.Repeat("a", 64) + `"}`,
		`{"rel_path":"missing-size.txt","name":"missing-size.txt","sha256":"` + strings.Repeat("b", 64) + `"}`,
	}, "\n")

	items, parseErrs, err := ReadManifest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("ReadManifest() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("item count = %d, want 1", len(items))
	}
	if items[0].Size != 0 {
		t.Fatalf("Size = %d, want 0", items[0].Size)
	}
	if len(parseErrs) != 1 {
		t.Fatalf("parse error count = %d, want 1", len(parseErrs))
	}
}

func TestReadManifestPreservesPathAndNameWhitespace(t *testing.T) {
	body := `{"rel_path":" dir/file .mkv","name":" file .mkv","size":1,"sha256":"` + strings.Repeat("a", 64) + `"}`

	items, parseErrs, err := ReadManifest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("ReadManifest() error = %v", err)
	}
	if len(parseErrs) != 0 {
		t.Fatalf("parse errors = %#v, want none", parseErrs)
	}
	if len(items) != 1 {
		t.Fatalf("item count = %d, want 1", len(items))
	}
	if items[0].RelPath != " dir/file .mkv" || items[0].Name != " file .mkv" {
		t.Fatalf("item = %#v", items[0])
	}
}
