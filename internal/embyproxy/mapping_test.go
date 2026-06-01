package embyproxy

import (
	"errors"
	"testing"
)

func TestNormalizeEmbyPathRejectsEscapes(t *testing.T) {
	bad := []string{
		"",
		"/media/../secret.mkv",
		"/media/%2e%2e/secret.mkv",
		"/media%2fsecret.mkv",
		"/media%5csecret.mkv",
		"//server/share/movie.mkv",
		"C:\\media\\movie.mkv",
		"%43%3a/media/movie.mkv",
		"%2f%2fserver/share/movie.mkv",
		"/media/movie\u0000.mkv",
		"/media/movie\u001f.mkv",
		"/media/movie∕evil.mkv",
	}
	for _, raw := range bad {
		if got, err := NormalizeEmbyPath(raw); err == nil {
			t.Fatalf("NormalizeEmbyPath(%q) = %q, nil error; want reject", raw, got)
		}
	}
}

func TestMapPathLongestPrefixAndSingleDecode(t *testing.T) {
	mappings := []MappingRule{
		{ID: 1, LibraryID: 10, PrefixNorm: "/media", EchoRelPrefix: "", CaseSensitive: true},
		{ID: 2, LibraryID: 11, PrefixNorm: "/media/movies", EchoRelPrefix: "movies", CaseSensitive: true},
		{ID: 3, LibraryID: 12, PrefixNorm: "/cases", EchoRelPrefix: "Cases", CaseSensitive: false},
	}
	got, err := MatchPath(mappings, "/media/movies/Film%20One.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if got.MappingID != 2 || got.LibraryID != 11 || got.RelPath != "movies/Film One.mkv" {
		t.Fatalf("match = %#v", got)
	}
	if _, err := MatchPath(mappings, "/media/movies/Film%252fOne.mkv"); err != nil {
		t.Fatalf("single percent decode should not treat %%252f as slash escape: %v", err)
	}
	if _, err := MatchPath(mappings, "/media2/Film.mkv"); !errors.Is(err, ErrNoMapping) {
		t.Fatalf("/media2 matched /media prefix, err=%v; want ErrNoMapping", err)
	}
	got, err = MatchPath(mappings, "/CASES/Drama/E01.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if got.MappingID != 3 || got.RelPath != "Cases/Drama/E01.mkv" {
		t.Fatalf("case-insensitive match = %#v", got)
	}
}

func TestMapPathRejectsUnsafeEchoRelPrefix(t *testing.T) {
	for _, prefix := range []string{"/abs", "../escape", "safe/../escape", "//unc"} {
		_, err := MatchPath([]MappingRule{{
			ID: 1, LibraryID: 10, PrefixNorm: "/media", EchoRelPrefix: prefix, CaseSensitive: true,
		}}, "/media/Film.mkv")
		if err == nil {
			t.Fatalf("EchoRelPrefix %q accepted, want reject", prefix)
		}
	}
}
