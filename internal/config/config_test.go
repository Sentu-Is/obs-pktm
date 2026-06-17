package config

import (
	"path/filepath"
	"testing"
)

func TestRecordingRenameConfigMinSizeBytes(t *testing.T) {
	cfg := RecordingRenameConfig{MinSizeMegabytes: 1.5}
	if got, want := cfg.MinSizeBytes(), int64(1572864); got != want {
		t.Fatalf("MinSizeBytes() = %d, want %d", got, want)
	}
}

func TestRecordingRenameRulesExpandsPortablePaths(t *testing.T) {
	t.Setenv("PKTM_TEST_HOME", `C:\Users\TestUser`)
	cfg := Config{
		RecordingRename: RecordingRenameConfig{
			Directory:            `${PKTM_TEST_HOME}\Videos\Work`,
			ManualShortDirectory: `%PKTM_TEST_HOME%\Videos\Work\TRASH`,
		},
	}

	rules := cfg.RecordingRenameRules()
	if got, want := rules.Directory, `C:\Users\TestUser\Videos\Work`; got != want {
		t.Fatalf("Directory = %q, want %q", got, want)
	}
	if got, want := rules.ManualShortDirectory, `C:\Users\TestUser\Videos\Work\TRASH`; got != want {
		t.Fatalf("ManualShortDirectory = %q, want %q", got, want)
	}
}

func TestExpandPathExpandsHomePrefix(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)

	got := ExpandPath(`~\Videos\Work`)
	want := filepath.Join(home, "Videos", "Work")
	if got != want {
		t.Fatalf("ExpandPath() = %q, want %q", got, want)
	}
}
