package rules

import "testing"

func TestParseFeatures(t *testing.T) {
	got := ParseFeatures("Movie.2024.2160p.DV.HDR10.TrueHD.Atmos.REMUX-GRP.mkv", "")
	if got.Resolution != "4K" || got.Color != "DV&HDR10" || got.Audio != "TRUEHD ATMOS" || got.Ext != "MKV" {
		t.Fatalf("unexpected features: %#v", got)
	}
}

func TestDisabledExtensionRejects(t *testing.T) {
	profile := Profile{
		Weights:     []string{"resolutions", "extensions"},
		Resolutions: []RuleItem{{Name: "4K", Enabled: true}},
		Extensions:  []RuleItem{{Name: "ISO", Enabled: false}},
	}
	score, decision := Score(ParseFeatures("Movie.2024.2160p.iso", ""), profile)
	if decision != Reject || len(score) != 2 {
		t.Fatalf("score=%v decision=%s, want complete score tuple and reject", score, decision)
	}
}

func TestKeywordWeightsParticipateInScore(t *testing.T) {
	profile := Profile{
		Weights:  []string{"keywords"},
		Keywords: []RuleItem{{Name: "Criterion", Enabled: true}},
	}
	score, decision := Score(ParseFeatures("Movie.2024.Criterion.2160p.mkv", ""), profile)
	if decision != Accept || len(score) != 1 || score[0] != 0 {
		t.Fatalf("score=%v decision=%s, want keyword accept", score, decision)
	}
}

func TestScoreTupleFollowsProfileOrder(t *testing.T) {
	profile := Profile{
		Weights: []string{"resolutions", "extensions", "groups"},
		Resolutions: []RuleItem{
			{Name: "1080P", Enabled: true},
			{Name: "4K", Enabled: true},
		},
		Extensions: []RuleItem{
			{Name: "MKV", Enabled: true},
			{Name: "ISO", Enabled: true},
		},
		Groups: []RuleItem{
			{Name: "OTHER", Enabled: true},
			{Name: "GRP", Enabled: true},
		},
	}

	score, decision := Score(ParseFeatures("Movie.2024.2160p.REMUX-GRP.mkv", ""), profile)
	want := []int{1, 0, 1}
	if decision != Accept || len(score) != len(want) {
		t.Fatalf("score=%v decision=%s, want %v accept", score, decision, want)
	}
	for idx := range want {
		if score[idx] != want[idx] {
			t.Fatalf("score=%v decision=%s, want %v accept", score, decision, want)
		}
	}
}

func TestValidateProfileRejectsInvalidProfiles(t *testing.T) {
	tests := []struct {
		name    string
		profile Profile
	}{
		{
			name:    "empty weights",
			profile: Profile{},
		},
		{
			name: "unknown weight",
			profile: Profile{
				Weights: []string{"bogus"},
			},
		},
		{
			name: "empty item name",
			profile: Profile{
				Weights:    []string{"keywords"},
				Keywords:   []RuleItem{{Name: " "}},
				Extensions: []RuleItem{{Name: "MKV", Enabled: true}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateProfile(tt.profile); err == nil {
				t.Fatalf("ValidateProfile() error = nil, want error")
			}
		})
	}
}
