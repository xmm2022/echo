package media

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestNormalizeSeasonFilterCanonicalizesOrder(t *testing.T) {
	normalized, key, err := NormalizeSeasonFilter(`[3,1,2,3]`)
	if err != nil {
		t.Fatalf("NormalizeSeasonFilter returned error: %v", err)
	}
	if normalized != `[1,2,3]` {
		t.Fatalf("normalized = %q, want %q", normalized, `[1,2,3]`)
	}
	if key != testSeasonFilterKey(`[1,2,3]`) {
		t.Fatalf("key = %q, want sha256 of normalized JSON", key)
	}
}

func TestNormalizeSeasonFilterRejectsInvalidJSONAndHugeLists(t *testing.T) {
	huge := "[" + strings.TrimRight(strings.Repeat("1,", 100), ",") + "]"
	for _, raw := range []string{
		`{`,
		`{}`,
		`1`,
		`[1.5]`,
		`[0]`,
		`[-1]`,
		`[100]`,
		`["1"]`,
		huge,
	} {
		t.Run(fmt.Sprintf("raw=%s", raw), func(t *testing.T) {
			_, _, err := NormalizeSeasonFilter(raw)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("NormalizeSeasonFilter error = %v, want %v", err, ErrInvalidRequest)
			}
		})
	}
}

func TestSeasonFilterKeyIsEmptyForAllSeasons(t *testing.T) {
	for _, raw := range []string{"", "   ", "null", "\nnull\t"} {
		t.Run(fmt.Sprintf("raw=%q", raw), func(t *testing.T) {
			normalized, key, err := NormalizeSeasonFilter(raw)
			if err != nil {
				t.Fatalf("NormalizeSeasonFilter returned error: %v", err)
			}
			if normalized != "" || key != "" {
				t.Fatalf("normalized/key = %q/%q, want empty all-seasons filter", normalized, key)
			}
		})
	}
}

func TestUnionSeasonFiltersKeepsAllSeasonsDominant(t *testing.T) {
	for _, tc := range []struct {
		name string
		a    string
		b    string
		want string
	}{
		{name: "left all seasons", a: "", b: "[1,2]", want: ""},
		{name: "right all seasons", a: "[2]", b: "null", want: ""},
		{name: "explicit union", a: "[3,1]", b: "[2,3]", want: "[1,2,3]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := UnionSeasonFilters(tc.a, tc.b)
			if err != nil {
				t.Fatalf("UnionSeasonFilters returned error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("UnionSeasonFilters = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMovieRequestsRejectSeasonFilter(t *testing.T) {
	if _, _, err := ValidateSeasonFilterForMedia("movie", "[1]"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("ValidateSeasonFilterForMedia movie error = %v, want %v", err, ErrInvalidRequest)
	}

	normalized, key, err := ValidateSeasonFilterForMedia("movie", "")
	if err != nil {
		t.Fatalf("ValidateSeasonFilterForMedia movie empty returned error: %v", err)
	}
	if normalized != "" || key != "" {
		t.Fatalf("movie empty normalized/key = %q/%q, want empty", normalized, key)
	}

	normalized, key, err = ValidateSeasonFilterForMedia("tv", "[2,1]")
	if err != nil {
		t.Fatalf("ValidateSeasonFilterForMedia tv returned error: %v", err)
	}
	if normalized != "[1,2]" || key != testSeasonFilterKey("[1,2]") {
		t.Fatalf("tv normalized/key = %q/%q, want normalized explicit filter", normalized, key)
	}
}

func testSeasonFilterKey(normalized string) string {
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}
