package tmdb

import (
	"strings"
	"unicode"
)

func MatchTitleYear(candidates []Media, title string, year int, mediaType string) (Media, bool) {
	title = normalizeTitle(title)
	if title == "" {
		return Media{}, false
	}
	for _, candidate := range candidates {
		if candidate.TMDBID == title && mediaTypeMatches(candidate, mediaType) {
			return candidate, true
		}
	}
	if year != 0 {
		for _, candidate := range candidates {
			if mediaTypeMatches(candidate, mediaType) && candidate.ReleaseYear == year && candidateTitleMatches(candidate, title) {
				return candidate, true
			}
		}
	}
	for _, candidate := range candidates {
		if mediaTypeMatches(candidate, mediaType) && candidateTitleMatches(candidate, title) {
			return candidate, true
		}
	}
	return Media{}, false
}

func mediaTypeMatches(candidate Media, mediaType string) bool {
	return mediaType == "" || candidate.MediaType == "" || candidate.MediaType == mediaType
}

func candidateTitleMatches(candidate Media, title string) bool {
	return normalizeTitle(candidate.Title) == title || normalizeTitle(candidate.OriginalTitle) == title
}

func normalizeTitle(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	space := false
	for _, r := range value {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteRune(r)
			space = false
		case unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r):
			space = b.Len() > 0
		}
	}
	return b.String()
}
