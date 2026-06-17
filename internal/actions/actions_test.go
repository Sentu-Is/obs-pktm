package actions

import (
	"os"
	"path/filepath"
	"testing"
)

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

func TestMoveFailedRecordingToTrashMovesVideoAndInputLog(t *testing.T) {
	dir := t.TempDir()
	trashDir := filepath.Join(dir, "TRASH")
	videoPath := filepath.Join(dir, "2026-06-17 05-28-32.mp4")
	inputLogPath := filepath.Join(dir, "2026-06-17 05-28-32.jsonl")

	if err := os.WriteFile(videoPath, []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inputLogPath, []byte("input"), 0600); err != nil {
		t.Fatal(err)
	}

	move, err := moveFailedRecordingToTrash(videoPath, trashDir)
	if err != nil {
		t.Fatal(err)
	}

	wantVideo := filepath.Join(trashDir, "fail1.mp4")
	wantInputLog := filepath.Join(trashDir, "fail1.jsonl")
	if move.VideoPath != wantVideo {
		t.Fatalf("VideoPath = %q, want %q", move.VideoPath, wantVideo)
	}
	if move.InputLogOldPath != inputLogPath {
		t.Fatalf("InputLogOldPath = %q, want %q", move.InputLogOldPath, inputLogPath)
	}
	if move.InputLogPath != wantInputLog {
		t.Fatalf("InputLogPath = %q, want %q", move.InputLogPath, wantInputLog)
	}
	assertExists(t, wantVideo)
	assertExists(t, wantInputLog)
	assertMissing(t, videoPath)
	assertMissing(t, inputLogPath)
}

func TestMoveFailedRecordingToTrashSkipsExistingFailNames(t *testing.T) {
	dir := t.TempDir()
	trashDir := filepath.Join(dir, "TRASH")
	if err := os.MkdirAll(trashDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trashDir, "fail1.mp4"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}

	videoPath := filepath.Join(dir, "recording.mp4")
	if err := os.WriteFile(videoPath, []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}

	move, err := moveFailedRecordingToTrash(videoPath, trashDir)
	if err != nil {
		t.Fatal(err)
	}

	wantVideo := filepath.Join(trashDir, "fail2.mp4")
	if move.VideoPath != wantVideo {
		t.Fatalf("VideoPath = %q, want %q", move.VideoPath, wantVideo)
	}
	assertExists(t, wantVideo)
}

func assertExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %q to exist: %v", path, err)
	}
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %q to be missing, stat err = %v", path, err)
	}
}
