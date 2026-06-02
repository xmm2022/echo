package rules

import (
	"encoding/json"
	"fmt"
	"strings"
)

var validWeights = map[string]bool{
	"resolutions": true,
	"colors":      true,
	"audios":      true,
	"extensions":  true,
	"sizes":       true,
	"groups":      true,
	"keywords":    true,
}

func ParseProfileJSON(body []byte) (Profile, error) {
	var profile Profile
	if err := json.Unmarshal(body, &profile); err != nil {
		return Profile{}, err
	}
	if err := ValidateProfile(profile); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

func MarshalProfileJSON(profile Profile) ([]byte, error) {
	if err := ValidateProfile(profile); err != nil {
		return nil, err
	}
	return json.Marshal(profile)
}

func ValidateProfile(profile Profile) error {
	if len(profile.Weights) == 0 {
		return fmt.Errorf("weights must not be empty")
	}
	for _, weight := range profile.Weights {
		if !validWeights[weight] {
			return fmt.Errorf("unknown weight: %s", weight)
		}
	}

	if err := validateItems("resolutions", profile.Resolutions); err != nil {
		return err
	}
	if err := validateItems("colors", profile.Colors); err != nil {
		return err
	}
	if err := validateItems("audios", profile.Audios); err != nil {
		return err
	}
	if err := validateItems("extensions", profile.Extensions); err != nil {
		return err
	}
	if err := validateItems("sizes", profile.Sizes); err != nil {
		return err
	}
	if err := validateItems("groups", profile.Groups); err != nil {
		return err
	}
	return validateItems("keywords", profile.Keywords)
}

func validateItems(name string, items []RuleItem) error {
	for idx, item := range items {
		if strings.TrimSpace(item.Name) == "" {
			return fmt.Errorf("%s[%d].name must not be empty", name, idx)
		}
	}
	return nil
}
