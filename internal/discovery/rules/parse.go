package rules

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type Features struct {
	Resolution string
	Color      string
	Audio      string
	Group      string
	Ext        string
	SizeGB     float64
	Keywords   []string
}

var (
	tokenRE = regexp.MustCompile(`[A-Za-z0-9+]+`)
	sizeRE  = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(gib|gb)`)
)

func ParseFeatures(name, rawText string) Features {
	text := strings.Join([]string{name, rawText}, " ")
	upper := strings.ToUpper(text)

	features := Features{
		Resolution: parseResolution(upper),
		Color:      parseColor(upper),
		Audio:      parseAudio(upper),
		Group:      parseGroup(name),
		Ext:        parseExt(name),
		SizeGB:     parseSizeGB(text),
		Keywords:   parseKeywords(text),
	}
	return features
}

func parseResolution(upper string) string {
	switch {
	case strings.Contains(upper, "2160P") || strings.Contains(upper, "UHD") || hasToken(upper, "4K"):
		return "4K"
	case strings.Contains(upper, "1080P") || strings.Contains(upper, "FHD"):
		return "1080P"
	case strings.Contains(upper, "720P") || strings.Contains(upper, "HDTV"):
		return "720P"
	case strings.Contains(upper, "480P"):
		return "480P"
	default:
		return ""
	}
}

func parseColor(upper string) string {
	hasDV := hasToken(upper, "DV") || strings.Contains(upper, "DOLBY VISION")
	hasHDR10Plus := strings.Contains(upper, "HDR10+")
	hasHDR10 := strings.Contains(upper, "HDR10")
	hasHDR := hasToken(upper, "HDR")

	parts := make([]string, 0, 2)
	if hasDV {
		parts = append(parts, "DV")
	}
	if hasHDR10Plus {
		parts = append(parts, "HDR10+")
	} else if hasHDR10 {
		parts = append(parts, "HDR10")
	} else if hasHDR {
		parts = append(parts, "HDR")
	}
	return strings.Join(parts, "&")
}

func parseAudio(upper string) string {
	parts := make([]string, 0, 2)
	switch {
	case strings.Contains(upper, "TRUEHD"):
		parts = append(parts, "TRUEHD")
	case strings.Contains(upper, "DTS-HD") || strings.Contains(upper, "DTSHD"):
		parts = append(parts, "DTS-HD")
	case hasToken(upper, "DTS"):
		parts = append(parts, "DTS")
	case hasToken(upper, "AAC"):
		parts = append(parts, "AAC")
	case hasToken(upper, "AC3") || strings.Contains(upper, "DDP"):
		parts = append(parts, "DDP")
	}
	if hasToken(upper, "ATMOS") {
		parts = append(parts, "ATMOS")
	}
	return strings.Join(parts, " ")
}

func parseGroup(name string) string {
	base := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	idx := strings.LastIndex(base, "-")
	if idx < 0 || idx == len(base)-1 {
		return ""
	}
	return strings.ToUpper(strings.TrimSpace(base[idx+1:]))
}

func parseExt(name string) string {
	ext := strings.TrimPrefix(filepath.Ext(name), ".")
	return strings.ToUpper(ext)
}

func parseSizeGB(text string) float64 {
	match := sizeRE.FindStringSubmatch(text)
	if len(match) != 3 {
		return 0
	}
	size, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return 0
	}
	return size
}

func parseKeywords(text string) []string {
	tokens := tokenRE.FindAllString(text, -1)
	keywords := make([]string, 0, len(tokens))
	seen := make(map[string]bool, len(tokens))
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		key := strings.ToUpper(token)
		if seen[key] {
			continue
		}
		seen[key] = true
		keywords = append(keywords, token)
	}
	return keywords
}

func hasToken(upper, token string) bool {
	return regexp.MustCompile(`(^|[^A-Z0-9])` + regexp.QuoteMeta(token) + `([^A-Z0-9]|$)`).MatchString(upper)
}
