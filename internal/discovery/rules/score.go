package rules

import "strings"

type RuleItem struct {
	Name    string  `json:"name"`
	Enabled bool    `json:"enabled"`
	Min     float64 `json:"min,omitempty"`
	Max     float64 `json:"max,omitempty"`
}

type Profile struct {
	Weights     []string   `json:"weights"`
	Resolutions []RuleItem `json:"resolutions"`
	Colors      []RuleItem `json:"colors"`
	Audios      []RuleItem `json:"audios"`
	Extensions  []RuleItem `json:"extensions"`
	Sizes       []RuleItem `json:"sizes"`
	Groups      []RuleItem `json:"groups"`
	Keywords    []RuleItem `json:"keywords"`
}

type Decision string

const (
	Accept Decision = "accept"
	Review Decision = "review"
	Reject Decision = "reject"
)

func Score(features Features, profile Profile) ([]int, Decision) {
	score := make([]int, 0, len(profile.Weights))
	decision := Accept

	for _, weight := range profile.Weights {
		idx, matched, rejected := scoreWeight(features, profile, weight)
		score = append(score, idx)
		if rejected {
			decision = Reject
		} else if !matched && decision != Reject {
			decision = Review
		}
	}

	return score, decision
}

func scoreWeight(features Features, profile Profile, weight string) (int, bool, bool) {
	switch weight {
	case "resolutions":
		return scoreScalar(features.Resolution, profile.Resolutions, exactMatch)
	case "colors":
		return scoreScalar(features.Color, profile.Colors, componentMatch)
	case "audios":
		return scoreScalar(features.Audio, profile.Audios, componentMatch)
	case "extensions":
		return scoreScalar(features.Ext, profile.Extensions, exactMatch)
	case "sizes":
		return scoreSize(features.SizeGB, profile.Sizes)
	case "groups":
		return scoreScalar(features.Group, profile.Groups, exactMatch)
	case "keywords":
		return scoreKeywords(features.Keywords, profile.Keywords)
	default:
		return 0, false, false
	}
}

func scoreScalar(value string, items []RuleItem, match func(string, string) bool) (int, bool, bool) {
	for _, item := range items {
		if !item.Enabled && match(value, item.Name) {
			return len(items), false, true
		}
	}
	for idx, item := range items {
		if item.Enabled && match(value, item.Name) {
			return idx, true, false
		}
	}
	return len(items), false, false
}

func scoreSize(sizeGB float64, items []RuleItem) (int, bool, bool) {
	if sizeGB <= 0 {
		return len(items), false, false
	}
	for _, item := range items {
		if !item.Enabled && sizeMatches(sizeGB, item) {
			return len(items), false, true
		}
	}
	for idx, item := range items {
		if item.Enabled && sizeMatches(sizeGB, item) {
			return idx, true, false
		}
	}
	return len(items), false, false
}

func scoreKeywords(keywords []string, items []RuleItem) (int, bool, bool) {
	for _, item := range items {
		if !item.Enabled && keywordMatches(keywords, item.Name) {
			return len(items), false, true
		}
	}
	for idx, item := range items {
		if item.Enabled && keywordMatches(keywords, item.Name) {
			return idx, true, false
		}
	}
	return len(items), false, false
}

func sizeMatches(sizeGB float64, item RuleItem) bool {
	if item.Min > 0 && sizeGB < item.Min {
		return false
	}
	if item.Max > 0 && sizeGB > item.Max {
		return false
	}
	return item.Min > 0 || item.Max > 0
}

func exactMatch(value, name string) bool {
	return strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(name))
}

func componentMatch(value, name string) bool {
	value = strings.ToUpper(strings.TrimSpace(value))
	name = strings.ToUpper(strings.TrimSpace(name))
	if value == "" || name == "" {
		return false
	}
	if value == name {
		return true
	}
	for _, part := range strings.FieldsFunc(value, func(r rune) bool {
		return r == '&' || r == ' '
	}) {
		if part == name {
			return true
		}
	}
	return false
}

func keywordMatches(keywords []string, name string) bool {
	for _, keyword := range keywords {
		if strings.EqualFold(keyword, name) {
			return true
		}
	}
	return false
}
