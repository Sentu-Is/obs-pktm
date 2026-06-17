package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	defaultConfigPath           = "config.json"
	defaultTimerDurationSeconds = 600
	defaultOBSURL               = "ws://localhost:4455"
	defaultRequestTimeout       = 5
)

type Config struct {
	Timer           TimerConfig           `json:"timer"`
	OBSWebSocket    OBSWebSocketConfig    `json:"obs_websocket"`
	RecordingRename RecordingRenameConfig `json:"recording_rename"`
	RecordingCheck  RecordingCheckConfig  `json:"recording_check"`
}

type TimerConfig struct {
	DurationSeconds int `json:"duration_seconds"`
}

type OBSWebSocketConfig struct {
	URL                   string `json:"url"`
	Password              string `json:"password"`
	RequestTimeoutSeconds int    `json:"request_timeout_seconds"`
}

type RecordingRenameConfig struct {
	Prefix                     string  `json:"prefix"`
	MinDurationSeconds         int     `json:"min_duration_seconds"`
	MinSizeMegabytes           float64 `json:"min_size_megabytes"`
	Directory                  string  `json:"directory"`
	ManualShortDurationSeconds int     `json:"manual_short_duration_seconds"`
	ManualShortDirectory       string  `json:"manual_short_directory"`
}

type RecordingCheckConfig struct {
	StartupDelaySeconds   int `json:"startup_delay_seconds"`
	StartupTimeoutSeconds int `json:"startup_timeout_seconds"`
}

func Load(path string) (Config, error) {
	if path == "" {
		path = defaultConfigPath
	}

	cfg := Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, Save(path, cfg)
	}
	if err != nil {
		return Config{}, err
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	cfg.applyDefaults()
	return cfg, nil
}

func Save(path string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0600)
}

func Default() Config {
	cfg := Config{
		Timer: TimerConfig{
			DurationSeconds: defaultTimerDurationSeconds,
		},
		OBSWebSocket: OBSWebSocketConfig{
			URL:                   defaultOBSURL,
			RequestTimeoutSeconds: defaultRequestTimeout,
		},
	}
	cfg.applyDefaults()
	return cfg
}

func (c Config) TimerDuration() time.Duration {
	if c.Timer.DurationSeconds <= 0 {
		return time.Duration(defaultTimerDurationSeconds) * time.Second
	}
	return time.Duration(c.Timer.DurationSeconds) * time.Second
}

func (c Config) OBSRequestTimeout() time.Duration {
	if c.OBSWebSocket.RequestTimeoutSeconds <= 0 {
		return time.Duration(defaultRequestTimeout) * time.Second
	}
	return time.Duration(c.OBSWebSocket.RequestTimeoutSeconds) * time.Second
}

func (c Config) RecordingRenameRules() RecordingRenameConfig {
	rules := c.RecordingRename
	rules.Directory = ExpandPath(rules.Directory)
	rules.ManualShortDirectory = ExpandPath(rules.ManualShortDirectory)
	return rules
}

func (r RecordingRenameConfig) MinSizeBytes() int64 {
	if r.MinSizeMegabytes <= 0 {
		return 0
	}
	return int64(r.MinSizeMegabytes * 1024 * 1024)
}

func (c Config) CompatibilityWarnings() []string {
	var warnings []string
	warnings = append(warnings, pathCompatibilityWarnings("recording_rename.directory", c.RecordingRename.Directory, true)...)
	warnings = append(warnings, pathCompatibilityWarnings("recording_rename.manual_short_directory", c.RecordingRename.ManualShortDirectory, false)...)
	return warnings
}

func ExpandPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}

	if path == "~" || strings.HasPrefix(path, "~\\") || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			if path == "~" {
				path = home
			} else {
				path = filepath.Join(home, path[2:])
			}
		}
	}

	path = expandWindowsEnv(path)
	return os.ExpandEnv(path)
}

func (c Config) RecordingCheckRules() RecordingCheckConfig {
	return c.RecordingCheck
}

func (c *Config) applyDefaults() {
	if c.Timer.DurationSeconds <= 0 {
		c.Timer.DurationSeconds = defaultTimerDurationSeconds
	}
	if c.OBSWebSocket.URL == "" {
		c.OBSWebSocket.URL = defaultOBSURL
	}
	if c.OBSWebSocket.RequestTimeoutSeconds <= 0 {
		c.OBSWebSocket.RequestTimeoutSeconds = defaultRequestTimeout
	}
}

func pathCompatibilityWarnings(name, path string, mustExist bool) []string {
	if strings.TrimSpace(path) == "" {
		return nil
	}

	var warnings []string
	expanded := ExpandPath(path)
	if containsUnexpandedVariable(expanded) {
		warnings = append(warnings, name+" contains an environment variable that could not be resolved: "+path)
	}
	if isDifferentUserProfilePath(expanded) {
		warnings = append(warnings, name+" appears to point to another user's profile: "+expanded)
	}
	if mustExist {
		if _, err := os.Stat(expanded); err != nil {
			warnings = append(warnings, name+" does not exist on this machine: "+expanded)
		}
		return warnings
	}

	parent := filepath.Dir(expanded)
	if parent != "." {
		if _, err := os.Stat(parent); err != nil {
			warnings = append(warnings, "the parent directory for "+name+" does not exist on this machine: "+parent)
		}
	}
	return warnings
}

func expandWindowsEnv(path string) string {
	envPattern := regexp.MustCompile(`%([^%]+)%`)
	return envPattern.ReplaceAllStringFunc(path, func(match string) string {
		name := strings.Trim(match, "%")
		if value, ok := os.LookupEnv(name); ok {
			return value
		}
		return match
	})
}

func containsUnexpandedVariable(path string) bool {
	return strings.Contains(path, "${") || regexp.MustCompile(`%[^%]+%`).MatchString(path)
}

func isDifferentUserProfilePath(path string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}

	cleanPath := strings.ToLower(filepath.Clean(path))
	cleanHome := strings.ToLower(filepath.Clean(home))
	if cleanPath == cleanHome || strings.HasPrefix(cleanPath, cleanHome+string(os.PathSeparator)) {
		return false
	}

	volume := filepath.VolumeName(cleanPath)
	rest := strings.TrimPrefix(cleanPath, volume)
	rest = strings.TrimLeft(rest, `\/`)
	parts := strings.FieldsFunc(rest, func(r rune) bool {
		return r == '\\' || r == '/'
	})
	return len(parts) >= 2 && parts[0] == "users"
}
