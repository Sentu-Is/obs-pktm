package actions

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"obs-pktm/internal/logging"
)

type Recorder interface {
	StartRecord() error
	StopRecord() (string, error)
	IsRecording() (bool, error)
	CurrentSceneName() (string, error)
	CurrentWindowName() (string, error)
	CurrentSceneScreenshot() ([]byte, error)
	RecordDirectory() (string, error)
}

type Timer interface {
	Start() error
	Stop()
}

type ErrorReporter func(title, message string)

type RenameRules struct {
	Prefix              string
	MinDuration         time.Duration
	MinSize             int64
	Directory           string
	ManualShortDuration time.Duration
	ManualShortDir      string
}

type StartupCheckRules struct {
	Delay   time.Duration
	Timeout time.Duration
}

type recordingState int

const (
	stateIdle recordingState = iota
	stateStarting
	stateRecording
	stateWaiting
	stateStopping

	recordStatusPollInterval = 250 * time.Millisecond
	stopCooldown             = time.Second

	blackPixelThreshold = 18
	blackAverageLimit   = 10
	blackBrightRatio    = 0.01

	inputLogExtension = ".jsonl"
)

type Library struct {
	recorder Recorder
	timer    Timer
	report   ErrorReporter
	rename   RenameRules
	check    StartupCheckRules
	logger   *logging.Logger

	opMu       sync.Mutex
	stateMu    sync.Mutex
	generation uint64
	state      recordingState

	activeScene string
	activeStart time.Time
}

func New(recorder Recorder, timer Timer, report ErrorReporter) *Library {
	return &Library{
		recorder: recorder,
		timer:    timer,
		report:   report,
	}
}

func (l *Library) SetRenameRules(rules RenameRules) {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()

	l.rename = rules
}

func (l *Library) SetStartupCheckRules(rules StartupCheckRules) {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()

	l.check = rules
}

func (l *Library) SetLogger(logger *logging.Logger) {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()

	l.logger = logger
}

func (l *Library) StartRecording() {
	generation, ok := l.beginStart()
	if !ok {
		l.logInfo("start_ignored", "start ignored because recorder is busy", nil)
		return
	}
	l.logInfo("start_requested", "start recording requested", map[string]any{"generation": generation})

	if err := l.timer.Start(); err != nil {
		l.finishStart(generation, false)
		l.showError(fmt.Sprintf("Could not start the RTSS timer:\n\n%s", err.Error()))
		return
	}

	go l.run("start recording", func() error {
		if err := l.startRecordChecked(generation); err != nil {
			return err
		}
		l.finishStart(generation, true)
		return nil
	})
}

func (l *Library) StopRecordingAndResetTimer() {
	generation, ok := l.beginStop()
	if !ok {
		l.logInfo("stop_ignored", "stop ignored because recorder is not recording", nil)
		return
	}
	l.logInfo("stop_requested", "stop recording requested", map[string]any{"generation": generation})

	l.timer.Stop()

	go l.run("stop recording", func() error {
		outputPath, err := l.recorder.StopRecord()
		l.finishStop(generation, err == nil)
		if err != nil {
			return err
		}
		return l.processManualCompletedRecording(generation, outputPath)
	})
}

func (l *Library) RestartRecordingAndResetTimer() {
	generation, ok := l.beginWaitForOBSStop()
	if !ok {
		l.logInfo("restart_ignored", "restart ignored because recorder is not recording", nil)
		return
	}
	l.logInfo("restart_waiting", "timer ended; waiting for OBS to finish current recording", map[string]any{"generation": generation})

	go l.run("restart recording", func() error {
		for {
			recording, err := l.recorder.IsRecording()
			if err != nil {
				l.finishRestart(generation, false, true)
				return err
			}
			if !recording {
				l.logInfo("obs_recording_finished", "OBS reports recording inactive", map[string]any{"generation": generation})
				break
			}
			time.Sleep(recordStatusPollInterval)
			if !l.isCurrentGeneration(generation) {
				return nil
			}
		}

		if err := l.renameCompletedRecording(generation, ""); err != nil {
			l.finishRestart(generation, false, false)
			return err
		}

		if err := l.timer.Start(); err != nil {
			l.finishRestart(generation, false, false)
			return fmt.Errorf("could not restart the RTSS timer: %w", err)
		}

		if err := l.startRecordChecked(generation); err != nil {
			return err
		}

		l.finishRestart(generation, true, false)
		return nil
	})
}

func (l *Library) run(action string, fn func() error) {
	l.opMu.Lock()
	defer l.opMu.Unlock()

	if err := fn(); err != nil {
		l.logError(action, err.Error(), nil)
		l.showError(fmt.Sprintf("Could not %s:\n\n%s", action, err.Error()))
	}
}

func (l *Library) logInfo(action, message string, fields map[string]any) {
	l.stateMu.Lock()
	logger := l.logger
	l.stateMu.Unlock()

	if logger != nil {
		logger.Info(action, message, fields)
	}
}

func (l *Library) logError(action, message string, fields map[string]any) {
	l.stateMu.Lock()
	logger := l.logger
	l.stateMu.Unlock()

	if logger != nil {
		logger.Error(action, message, fields)
	}
}

func (l *Library) startRecordChecked(generation uint64) error {
	l.logInfo("obs_start_record", "calling OBS StartRecord", map[string]any{"generation": generation})
	if err := l.recorder.StartRecord(); err != nil {
		if l.isCurrentGeneration(generation) {
			l.timer.Stop()
		}
		l.finishStart(generation, false)
		return err
	}

	recording, err := l.waitForRecordingStarted(generation)
	if err != nil {
		l.finishRecordingStarted(generation)
		return fmt.Errorf("could not verify whether OBS started recording: %w", err)
	}
	if !recording {
		if l.isCurrentGeneration(generation) {
			l.timer.Stop()
		}
		l.finishStart(generation, false)
		return fmt.Errorf("OBS did not start recording")
	}
	l.logInfo("obs_recording_active", "OBS reports recording active", map[string]any{"generation": generation})

	sceneName, err := l.recorder.CurrentSceneName()
	if err != nil {
		l.finishRecordingStarted(generation)
		return fmt.Errorf("could not read the current OBS scene: %w", err)
	}
	l.logInfo("scene_detected", "current OBS scene detected", map[string]any{"generation": generation, "scene": sceneName})

	recordingName, err := l.recorder.CurrentWindowName()
	if err != nil {
		l.logError("window_name_failed", "could not detect scene window name; falling back to scene name", map[string]any{
			"generation": generation,
			"scene":      sceneName,
			"error":      err.Error(),
		})
		recordingName = sceneName
	}
	l.logInfo("recording_name_detected", "recording name detected", map[string]any{
		"generation":     generation,
		"scene":          sceneName,
		"recording_name": recordingName,
	})

	screenshot, err := l.recorder.CurrentSceneScreenshot()
	if err != nil {
		l.finishRecordingStarted(generation)
		return fmt.Errorf("could not capture the current OBS scene: %w", err)
	}

	black, err := isBlackScreenshot(screenshot)
	if err != nil {
		l.finishRecordingStarted(generation)
		return fmt.Errorf("could not analyze the OBS screenshot: %w", err)
	}
	if !black {
		startedAt := time.Now()
		l.setActiveRecording(generation, recordingName, startedAt)
		l.logInfo("scene_check_passed", "scene screenshot is not black", map[string]any{
			"generation":     generation,
			"scene":          sceneName,
			"recording_name": recordingName,
			"started_at":     startedAt.Format(time.RFC3339Nano),
		})
		return nil
	}
	l.logError("scene_check_failed", "scene screenshot is black", map[string]any{"generation": generation, "scene": sceneName})

	if l.isCurrentGeneration(generation) {
		l.timer.Stop()
	}

	outputPath, stopErr := l.recorder.StopRecord()
	if stopErr != nil {
		l.finishRecordingStarted(generation)
		return fmt.Errorf("the current scene is black; additionally, the failed recording could not be stopped: %w", stopErr)
	}

	failPath, renameErr := renameFailedRecording(outputPath)
	l.finishRecordingStopped(generation)
	if renameErr != nil {
		l.logError("failed_recording_rename_failed", "black scene recording rename failed", map[string]any{
			"generation": generation,
			"old_path":   outputPath,
			"error":      renameErr.Error(),
		})
		return fmt.Errorf("the current scene is black; the failed recording was stopped, but %q could not be renamed: %w", outputPath, renameErr)
	}

	l.logError("failed_recording_renamed", "black scene recording renamed", map[string]any{
		"generation": generation,
		"old_path":   outputPath,
		"new_path":   failPath,
	})
	return fmt.Errorf("the current scene is black; recording was stopped and the file was renamed to %s", failPath)
}

func (l *Library) startupCheckRules() StartupCheckRules {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()

	rules := l.check
	if rules.Delay <= 0 {
		rules.Delay = time.Second
	}
	if rules.Timeout <= 0 {
		rules.Timeout = 5 * time.Second
	}
	return rules
}

func (l *Library) waitForRecordingStarted(generation uint64) (bool, error) {
	rules := l.startupCheckRules()
	if rules.Delay > 0 {
		l.logInfo("startup_check_delay", "waiting before checking OBS recording status", map[string]any{
			"generation":    generation,
			"delay_seconds": rules.Delay.Seconds(),
		})
		time.Sleep(rules.Delay)
	}
	if !l.isCurrentGeneration(generation) {
		return false, nil
	}

	deadline := time.Now().Add(rules.Timeout)
	var lastErr error
	for {
		recording, err := l.recorder.IsRecording()
		if err == nil {
			if recording {
				return true, nil
			}
		} else {
			lastErr = err
		}

		if time.Now().After(deadline) {
			if lastErr != nil {
				return false, lastErr
			}
			return false, nil
		}

		time.Sleep(recordStatusPollInterval)
		if !l.isCurrentGeneration(generation) {
			return false, nil
		}
	}
}

func (l *Library) setActiveRecording(generation uint64, scene string, startedAt time.Time) {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()

	if l.generation == generation {
		l.activeScene = scene
		l.activeStart = startedAt
	}
}

func (l *Library) activeRecordingInfo(generation uint64) (RenameRules, string, time.Time, bool) {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()

	if l.generation != generation {
		return RenameRules{}, "", time.Time{}, false
	}
	return l.rename, l.activeScene, l.activeStart, true
}

func (l *Library) processManualCompletedRecording(generation uint64, outputPath string) error {
	moved, err := l.moveManualShortRecording(generation, outputPath)
	if err != nil {
		return err
	}
	if moved {
		return nil
	}
	return l.renameCompletedRecording(generation, outputPath)
}

func (l *Library) moveManualShortRecording(generation uint64, outputPath string) (bool, error) {
	rules, scene, startedAt, ok := l.activeRecordingInfo(generation)
	if !ok || rules.ManualShortDuration <= 0 {
		return false, nil
	}
	if startedAt.IsZero() {
		l.logError("manual_short_move_skipped", "recording start time is missing", map[string]any{
			"generation": generation,
		})
		return false, nil
	}

	duration := time.Since(startedAt)
	if duration >= rules.ManualShortDuration {
		l.logInfo("manual_short_move_skipped", "manual recording duration is above configured short threshold", map[string]any{
			"generation":              generation,
			"duration_seconds":        duration.Seconds(),
			"short_threshold_seconds": rules.ManualShortDuration.Seconds(),
		})
		return false, nil
	}
	if outputPath == "" {
		return false, fmt.Errorf("OBS did not return an output path for the short manual recording")
	}
	if rules.ManualShortDir == "" {
		rules.ManualShortDir = filepath.Join(filepath.Dir(outputPath), "TRASH")
		l.logInfo("manual_short_directory_defaulted", "manual short recording directory defaulted next to OBS output", map[string]any{
			"generation": generation,
			"directory":  rules.ManualShortDir,
		})
	}

	info, err := waitForFile(outputPath)
	if err != nil {
		return false, fmt.Errorf("could not read the short recording %q: %w", outputPath, err)
	}
	if err := os.MkdirAll(rules.ManualShortDir, 0755); err != nil {
		return false, fmt.Errorf("could not create the short-recordings directory %q: %w", rules.ManualShortDir, err)
	}

	inputLog, hasInputLog := findInputLog(outputPath)
	targetVideo := filepath.Join(rules.ManualShortDir, filepath.Base(outputPath))

	l.logInfo("manual_short_move_started", "moving short manual recording", map[string]any{
		"generation":              generation,
		"scene":                   scene,
		"old_path":                outputPath,
		"new_path":                targetVideo,
		"duration_seconds":        duration.Seconds(),
		"short_threshold_seconds": rules.ManualShortDuration.Seconds(),
		"size_bytes":              info.Size(),
	})
	if err := renameWithRetry(outputPath, targetVideo); err != nil {
		l.logError("manual_short_video_move_failed", "short manual recording video move failed", map[string]any{
			"old_path": outputPath,
			"new_path": targetVideo,
			"error":    err.Error(),
		})
		return false, fmt.Errorf("could not move the short recording from %q to %q: %w", outputPath, targetVideo, err)
	}
	l.logInfo("manual_short_video_moved", "short manual recording video moved", map[string]any{
		"old_path": outputPath,
		"new_path": targetVideo,
	})

	if hasInputLog {
		targetLog := filepath.Join(rules.ManualShortDir, filepath.Base(inputLog))
		l.logInfo("manual_short_inputlog_move_started", "moving short manual recording input log", map[string]any{
			"old_path": inputLog,
			"new_path": targetLog,
		})
		if err := renameWithRetry(inputLog, targetLog); err != nil {
			l.logError("manual_short_inputlog_move_failed", "short manual recording input log move failed", map[string]any{
				"old_path": inputLog,
				"new_path": targetLog,
				"error":    err.Error(),
			})
			return false, fmt.Errorf("the short video was moved to %q, but the input log could not be moved from %q to %q: %w", targetVideo, inputLog, targetLog, err)
		}
		l.logInfo("manual_short_inputlog_moved", "short manual recording input log moved", map[string]any{
			"old_path": inputLog,
			"new_path": targetLog,
		})
	}

	return true, nil
}

func (l *Library) renameCompletedRecording(generation uint64, outputPath string) error {
	rules, scene, startedAt, ok := l.activeRecordingInfo(generation)
	if !ok {
		l.logInfo("rename_skipped", "recording state is no longer current", map[string]any{
			"generation": generation,
		})
		return nil
	}
	if rules.Prefix == "" {
		l.logInfo("rename_skipped", "recording rename is disabled because prefix is empty", map[string]any{
			"generation": generation,
		})
		return nil
	}
	if scene == "" || startedAt.IsZero() {
		l.logError("rename_skipped", "recording rename missing scene or start time", map[string]any{
			"generation": generation,
			"scene":      scene,
			"started_at": startedAt.Format(time.RFC3339Nano),
		})
		return nil
	}

	l.logInfo("rename_started", "recording rename post-process started", map[string]any{
		"generation":  generation,
		"output_path": outputPath,
		"scene":       scene,
		"prefix":      rules.Prefix,
		"started_at":  startedAt.Format(time.RFC3339Nano),
	})

	if outputPath == "" {
		dir := rules.Directory
		if dir == "" {
			var err error
			dir, err = l.recorder.RecordDirectory()
			if err != nil {
				return fmt.Errorf("could not get the OBS recording directory; configure recording_rename.directory: %w", err)
			}
		}
		if _, err := os.Stat(dir); err != nil {
			l.logError("recording_directory_invalid", "configured recording directory does not exist; trying OBS record directory", map[string]any{
				"generation": generation,
				"directory":  dir,
				"error":      err.Error(),
			})
			obsDir, obsErr := l.recorder.RecordDirectory()
			if obsErr != nil {
				return fmt.Errorf("recording directory %q was not found and the OBS recording directory could not be read: %w", dir, obsErr)
			}
			dir = obsDir
			if _, statErr := os.Stat(dir); statErr != nil {
				return fmt.Errorf("OBS returned a recording directory that does not exist %q: %w", dir, statErr)
			}
		}
		l.logInfo("recording_search", "searching latest recording by directory", map[string]any{
			"generation": generation,
			"directory":  dir,
			"started_at": startedAt.Format(time.RFC3339Nano),
		})
		found, err := latestRecordingSince(dir, startedAt)
		if err != nil {
			return fmt.Errorf("no recording file was found in %q: %w", dir, err)
		}
		outputPath = found
		l.logInfo("recording_found", "latest recording file selected", map[string]any{
			"generation": generation,
			"path":       outputPath,
		})
	}

	if strings.ToLower(filepath.Ext(outputPath)) != ".mp4" {
		l.logInfo("rename_skipped", "recording is not an mp4", map[string]any{"path": outputPath})
		return nil
	}

	info, err := waitForFile(outputPath)
	if err != nil {
		return fmt.Errorf("could not read the recorded file %q: %w", outputPath, err)
	}

	duration := time.Since(startedAt)
	if rules.MinDuration > 0 && duration < rules.MinDuration {
		l.logInfo("rename_skipped", "recording duration is below configured minimum", map[string]any{
			"path":                 outputPath,
			"duration_seconds":     duration.Seconds(),
			"min_duration_seconds": rules.MinDuration.Seconds(),
		})
		return nil
	}
	if rules.MinSize > 0 && info.Size() < rules.MinSize {
		l.logInfo("rename_skipped", "recording size is below configured minimum", map[string]any{
			"path":           outputPath,
			"size_bytes":     info.Size(),
			"min_size_bytes": rules.MinSize,
		})
		return nil
	}

	normalizedName := normalizedRecordingName(scene)
	targetBase, err := nextRecordingBase(filepath.Dir(outputPath), rules.Prefix, normalizedName)
	if err != nil {
		return err
	}
	l.logInfo("rename_target_selected", "recording rename target selected", map[string]any{
		"generation":      generation,
		"source_path":     outputPath,
		"target_base":     targetBase,
		"raw_name":        scene,
		"normalized_name": normalizedName,
	})

	targetVideo := targetBase + ".mp4"
	inputLog, hasInputLog := findInputLog(outputPath)
	l.logInfo("recording_rename_attempt", "renaming recording video", map[string]any{
		"old_path": outputPath,
		"new_path": targetVideo,
	})
	if err := renameWithRetry(outputPath, targetVideo); err != nil {
		l.logError("recording_rename_failed", "recording video rename failed", map[string]any{
			"old_path": outputPath,
			"new_path": targetVideo,
			"error":    err.Error(),
		})
		return fmt.Errorf("could not rename %q to %q: %w", outputPath, targetVideo, err)
	}
	l.logInfo("recording_renamed", "recording video renamed", map[string]any{
		"old_path": outputPath,
		"new_path": targetVideo,
		"scene":    scene,
	})

	if hasInputLog {
		targetLog := targetBase + inputLogExtension
		l.logInfo("inputlog_rename_attempt", "renaming input log", map[string]any{
			"old_path": inputLog,
			"new_path": targetLog,
		})
		if err := renameWithRetry(inputLog, targetLog); err != nil {
			l.logError("inputlog_rename_failed", "input log rename failed", map[string]any{
				"old_path": inputLog,
				"new_path": targetLog,
				"error":    err.Error(),
			})
			return fmt.Errorf("could not rename %q to %q: %w", inputLog, targetLog, err)
		}
		l.logInfo("inputlog_renamed", "input log renamed", map[string]any{
			"old_path": inputLog,
			"new_path": targetLog,
		})
	} else {
		l.logError("inputlog_missing", "no input log found for recording", map[string]any{
			"video_path": targetVideo,
		})
	}

	return nil
}

func (l *Library) beginStart() (uint64, bool) {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()

	if l.state != stateIdle {
		return l.generation, false
	}

	l.generation++
	l.state = stateStarting
	return l.generation, true
}

func (l *Library) finishStart(generation uint64, success bool) {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()

	if l.generation != generation {
		return
	}
	if success {
		l.state = stateRecording
		return
	}
	l.state = stateIdle
}

func (l *Library) finishRecordingStarted(generation uint64) {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()

	if l.generation == generation {
		l.state = stateRecording
	}
}

func (l *Library) finishRecordingStopped(generation uint64) {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()

	if l.generation == generation {
		l.state = stateIdle
	}
}

func (l *Library) beginStop() (uint64, bool) {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()

	if l.state != stateRecording && l.state != stateWaiting {
		return l.generation, false
	}

	l.generation++
	l.state = stateStopping
	return l.generation, true
}

func (l *Library) beginWaitForOBSStop() (uint64, bool) {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()

	if l.state != stateRecording {
		return l.generation, false
	}

	l.generation++
	l.state = stateWaiting
	return l.generation, true
}

func (l *Library) finishStop(generation uint64, success bool) {
	l.stateMu.Lock()

	if l.generation != generation {
		l.stateMu.Unlock()
		return
	}
	if !success {
		l.state = stateRecording
		l.stateMu.Unlock()
		return
	}

	l.stateMu.Unlock()

	time.AfterFunc(stopCooldown, func() {
		l.finishStopCooldown(generation)
	})
}

func (l *Library) finishStopCooldown(generation uint64) {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()

	if l.generation == generation && l.state == stateStopping {
		l.state = stateIdle
	}
}

func (l *Library) finishRestart(generation uint64, success, stillRecording bool) {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()

	if l.generation != generation {
		return
	}
	switch {
	case success:
		l.state = stateRecording
	case stillRecording:
		l.state = stateWaiting
	default:
		l.state = stateIdle
	}
}

func (l *Library) isCurrentGeneration(generation uint64) bool {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()

	return l.generation == generation
}

func (l *Library) showError(message string) {
	if l.report != nil {
		l.report("obs-pktm", message)
	}
}

func isBlackScreenshot(data []byte) (bool, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return false, err
	}

	bounds := img.Bounds()
	total := bounds.Dx() * bounds.Dy()
	if total <= 0 {
		return false, fmt.Errorf("screenshot has no pixels")
	}

	var lumaSum uint64
	var bright int

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			r8 := uint8(r >> 8)
			g8 := uint8(g >> 8)
			b8 := uint8(b >> 8)
			luma := int(r8)*299/1000 + int(g8)*587/1000 + int(b8)*114/1000
			lumaSum += uint64(luma)
			if luma > blackPixelThreshold {
				bright++
			}
		}
	}

	average := float64(lumaSum) / float64(total)
	brightRatio := float64(bright) / float64(total)
	return average <= blackAverageLimit && brightRatio <= blackBrightRatio, nil
}

func renameFailedRecording(outputPath string) (string, error) {
	if outputPath == "" {
		return "", fmt.Errorf("OBS did not return the recorded file path")
	}

	dir := filepath.Dir(outputPath)
	ext := filepath.Ext(outputPath)
	target, err := nextFailPath(dir, ext)
	if err != nil {
		return "", err
	}

	var lastErr error
	for i := 0; i < 10; i++ {
		if err := os.Rename(outputPath, target); err == nil {
			return target, nil
		} else {
			lastErr = err
		}
		time.Sleep(200 * time.Millisecond)
	}
	return "", lastErr
}

func nextFailPath(dir, ext string) (string, error) {
	for i := 1; i < 10000; i++ {
		path := filepath.Join(dir, fmt.Sprintf("fail%d%s", i, ext))
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("no hay nombres fail disponibles en %s", dir)
}

func latestRecordingSince(dir string, startedAt time.Time) (string, error) {
	var bestPath string
	var bestTime time.Time
	cutoff := startedAt.Add(-5 * time.Second)

	var lastErr error
	for attempt := 0; attempt < 10; attempt++ {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return "", err
		}

		for _, entry := range entries {
			if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".mp4" {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			info, err := entry.Info()
			if err != nil {
				lastErr = err
				continue
			}
			if info.ModTime().Before(cutoff) {
				continue
			}
			if bestPath == "" || info.ModTime().After(bestTime) {
				bestPath = path
				bestTime = info.ModTime()
			}
		}
		if bestPath != "" {
			return bestPath, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("no se encontro un .mp4 nuevo en %s", dir)
}

func waitForFile(path string) (os.FileInfo, error) {
	var previousSize int64 = -1
	var info os.FileInfo
	var lastErr error

	for i := 0; i < 10; i++ {
		stat, err := os.Stat(path)
		if err != nil {
			lastErr = err
			time.Sleep(200 * time.Millisecond)
			continue
		}
		info = stat
		if stat.Size() == previousSize {
			return stat, nil
		}
		previousSize = stat.Size()
		time.Sleep(200 * time.Millisecond)
	}
	if info != nil {
		return info, nil
	}
	return nil, lastErr
}

func nextRecordingBase(dir, prefix, name string) (string, error) {
	stem := sanitizeFilename(prefix + name)
	if stem == "" {
		return "", fmt.Errorf("destination name is empty")
	}

	for i := 1; i < 10000; i++ {
		base := filepath.Join(dir, fmt.Sprintf("%s[%d]", stem, i))
		if !recordingTargetsExist(base) {
			return base, nil
		}
	}
	return "", fmt.Errorf("no hay nombres disponibles para %s en %s", stem, dir)
}

func recordingTargetsExist(base string) bool {
	candidates := []string{
		base + ".mp4",
		base + ".jsonl",
		base + ".inputlog.jsonl",
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return true
		}
	}
	return false
}

func normalizedRecordingName(raw string) string {
	name := selectRecordingNamePart(raw)
	name = strings.TrimSuffix(name, filepath.Ext(name))
	tokens := nameTokens(name)
	for i, token := range tokens {
		if value, ok := romanToInt(strings.ToUpper(token)); ok {
			tokens[i] = fmt.Sprintf("%d", value)
		}
	}
	return strings.Join(tokens, "")
}

func selectRecordingNamePart(raw string) string {
	parts := strings.Split(raw, ":")
	for _, part := range parts {
		candidate := cleanWindowPart(part)
		if candidate == "" || looksLikeExecutable(candidate) {
			continue
		}
		return candidate
	}
	return cleanWindowPart(raw)
}

func cleanWindowPart(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "[]")
	return strings.TrimSpace(value)
}

func looksLikeExecutable(value string) bool {
	ext := strings.ToLower(filepath.Ext(strings.Trim(value, "[]")))
	switch ext {
	case ".exe", ".com", ".bat", ".cmd", ".msi":
		return true
	default:
		return false
	}
}

func nameTokens(value string) []string {
	var tokens []string
	var b strings.Builder

	flush := func() {
		if b.Len() == 0 {
			return
		}
		tokens = append(tokens, b.String())
		b.Reset()
	}

	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return tokens
}

func romanToInt(value string) (int, bool) {
	if value == "" {
		return 0, false
	}

	total := 0
	previous := 0
	for i := len(value) - 1; i >= 0; i-- {
		current, ok := romanDigit(value[i])
		if !ok {
			return 0, false
		}
		if current < previous {
			total -= current
		} else {
			total += current
			previous = current
		}
	}
	if total <= 0 || total > 3999 || intToRoman(total) != value {
		return 0, false
	}
	return total, true
}

func romanDigit(ch byte) (int, bool) {
	switch ch {
	case 'I':
		return 1, true
	case 'V':
		return 5, true
	case 'X':
		return 10, true
	case 'L':
		return 50, true
	case 'C':
		return 100, true
	case 'D':
		return 500, true
	case 'M':
		return 1000, true
	default:
		return 0, false
	}
}

func intToRoman(value int) string {
	pairs := []struct {
		value int
		text  string
	}{
		{1000, "M"},
		{900, "CM"},
		{500, "D"},
		{400, "CD"},
		{100, "C"},
		{90, "XC"},
		{50, "L"},
		{40, "XL"},
		{10, "X"},
		{9, "IX"},
		{5, "V"},
		{4, "IV"},
		{1, "I"},
	}

	var b strings.Builder
	for _, pair := range pairs {
		for value >= pair.value {
			b.WriteString(pair.text)
			value -= pair.value
		}
	}
	return b.String()
}

func sanitizeFilename(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		switch r {
		case '<', '>', ':', '"', '/', '\\', '|', '?', '*':
			b.WriteByte('_')
		default:
			if r >= 0 && r < 32 {
				b.WriteByte('_')
			} else {
				b.WriteRune(r)
			}
		}
	}

	clean := strings.TrimSpace(b.String())
	return strings.TrimRight(clean, ". ")
}

func findInputLog(videoPath string) (string, bool) {
	dir := filepath.Dir(videoPath)
	base := filepath.Base(videoPath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	candidates := []string{
		filepath.Join(dir, stem+".inputlog.jsonl"),
		filepath.Join(dir, stem+".jsonl"),
		filepath.Join(dir, base+".inputlog.jsonl"),
		filepath.Join(dir, base+".jsonl"),
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
	}

	videoInfo, err := os.Stat(videoPath)
	if err != nil {
		return "", false
	}

	var bestPath string
	var bestDelta time.Duration
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".jsonl") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		delta := info.ModTime().Sub(videoInfo.ModTime())
		if delta < 0 {
			delta = -delta
		}
		if delta > 10*time.Second {
			continue
		}
		if bestPath == "" || delta < bestDelta {
			bestPath = filepath.Join(dir, entry.Name())
			bestDelta = delta
		}
	}
	return bestPath, bestPath != ""
}

func renameWithRetry(oldPath, newPath string) error {
	var lastErr error
	for i := 0; i < 10; i++ {
		if err := os.Rename(oldPath, newPath); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(200 * time.Millisecond)
	}
	return lastErr
}
