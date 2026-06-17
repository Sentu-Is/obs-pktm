package actions

import "testing"

func TestNormalizedRecordingName(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "uses first title segment before colons",
			raw:  "Watch Dogs Legion:NomadWD2:WatchDogsLegion.exe",
			want: "WatchDogsLegion",
		},
		{
			name: "skips executable segment from OBS window selector",
			raw:  "[WatchDogsLegion.exe]:Watch Dogs Legion",
			want: "WatchDogsLegion",
		},
		{
			name: "removes extension from single segment",
			raw:  "Final Fantasy XIV.exe",
			want: "FinalFantasy14",
		},
		{
			name: "normalizes roman numeral tokens",
			raw:  "Resident Evil VII:Biohazard.exe",
			want: "ResidentEvil7",
		},
		{
			name: "removes separators and spaces",
			raw:  "Game - Part IV",
			want: "GamePart4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizedRecordingName(tt.raw); got != tt.want {
				t.Fatalf("normalizedRecordingName(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
