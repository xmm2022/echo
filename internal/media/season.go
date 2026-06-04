package media

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

func NormalizeSeasonFilter(raw string) (normalized string, key string, err error) {
	_, normalized, key, allSeasons, err := normalizeSeasonFilter(raw)
	if err != nil {
		return "", "", err
	}
	if allSeasons {
		return "", "", nil
	}
	return normalized, key, nil
}

func UnionSeasonFilters(a string, b string) (string, error) {
	aSeasons, _, _, aAll, err := normalizeSeasonFilter(a)
	if err != nil {
		return "", err
	}
	bSeasons, _, _, bAll, err := normalizeSeasonFilter(b)
	if err != nil {
		return "", err
	}
	if aAll || bAll {
		return "", nil
	}

	seen := make(map[int]struct{}, len(aSeasons)+len(bSeasons))
	for _, season := range aSeasons {
		seen[season] = struct{}{}
	}
	for _, season := range bSeasons {
		seen[season] = struct{}{}
	}

	union := make([]int, 0, len(seen))
	for season := range seen {
		union = append(union, season)
	}
	sort.Ints(union)

	normalized, _, err := marshalSeasonFilter(union)
	if err != nil {
		return "", err
	}
	return normalized, nil
}

func ValidateSeasonFilterForMedia(mediaType string, raw string) (normalized string, key string, err error) {
	normalized, key, err = NormalizeSeasonFilter(raw)
	if err != nil {
		return "", "", err
	}

	switch mediaType {
	case "movie":
		if normalized != "" {
			return "", "", fmt.Errorf("%w: movie requests cannot include a season filter", ErrInvalidRequest)
		}
		return "", "", nil
	case "tv":
		return normalized, key, nil
	default:
		return "", "", fmt.Errorf("%w: media_type must be movie or tv", ErrInvalidRequest)
	}
}

func normalizeSeasonFilter(raw string) (seasons []int, normalized string, key string, allSeasons bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil, "", "", true, nil
	}

	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()

	var values []any
	if err := decoder.Decode(&values); err != nil {
		return nil, "", "", false, fmt.Errorf("%w: season filter must be a JSON array of integers", ErrInvalidRequest)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, "", "", false, fmt.Errorf("%w: season filter must contain one JSON value", ErrInvalidRequest)
	}
	if len(values) > 99 {
		return nil, "", "", false, fmt.Errorf("%w: season filter cannot contain more than 99 seasons", ErrInvalidRequest)
	}

	seen := make(map[int]struct{}, len(values))
	for _, value := range values {
		number, ok := value.(json.Number)
		if !ok {
			return nil, "", "", false, fmt.Errorf("%w: season filter values must be numbers", ErrInvalidRequest)
		}
		season, err := number.Int64()
		if err != nil {
			return nil, "", "", false, fmt.Errorf("%w: season filter values must be integers", ErrInvalidRequest)
		}
		if season < 1 || season > 99 {
			return nil, "", "", false, fmt.Errorf("%w: season must be between 1 and 99", ErrInvalidRequest)
		}
		seen[int(season)] = struct{}{}
	}

	seasons = make([]int, 0, len(seen))
	for season := range seen {
		seasons = append(seasons, season)
	}
	sort.Ints(seasons)

	normalized, key, err = marshalSeasonFilter(seasons)
	if err != nil {
		return nil, "", "", false, err
	}
	return seasons, normalized, key, false, nil
}

func marshalSeasonFilter(seasons []int) (normalized string, key string, err error) {
	data, err := json.Marshal(seasons)
	if err != nil {
		return "", "", fmt.Errorf("%w: normalize season filter", ErrInvalidRequest)
	}
	sum := sha256.Sum256(data)
	return string(data), hex.EncodeToString(sum[:]), nil
}
