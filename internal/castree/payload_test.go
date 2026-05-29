package castree

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPayloadRoundTrip(t *testing.T) {
	want := Payload{
		Provider:   "115",
		Name:       "Film.mkv",
		Size:       123456789,
		MD5:        "",
		SliceMD5:   "",
		SHA1:       "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		PreID:      "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
		CreateTime: "1710000000",
	}

	encoded, err := Encode(want)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	got, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if got != want {
		t.Fatalf("Decode(Encode()) = %#v, want %#v", got, want)
	}
}

func TestPayloadDecodesCasTools115Fixture(t *testing.T) {
	body := readFixture(t, "115-film.mkv.cas")

	got, err := Decode(body)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if got.Provider != "115" || got.Name != "Film.mkv" || got.Size != 123456789 {
		t.Fatalf("decoded payload = %#v", got)
	}
	if got.SHA1 != "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" {
		t.Fatalf("SHA1 = %q", got.SHA1)
	}
	if got.PreID != "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB" {
		t.Fatalf("PreID = %q", got.PreID)
	}
	if got.CreateTime == "" {
		t.Fatal("CreateTime is empty")
	}
}

func TestPayloadDecodesCasTools139Fixture(t *testing.T) {
	body := readFixture(t, "139-x.txt.cas")

	got, err := Decode(body)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if got.Name != "x.txt" || got.Size != 12 {
		t.Fatalf("decoded payload = %#v", got)
	}
	if got.SHA256 != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("SHA256 = %q", got.SHA256)
	}
	if got.CreateTime == "" {
		t.Fatal("CreateTime is empty")
	}
}

func TestPayloadDecodeRejectsInvalidBase64(t *testing.T) {
	if _, err := Decode([]byte("not base64")); err == nil {
		t.Fatal("Decode() error = nil, want error")
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}
