//go:build windows

package main

import (
	"fmt"
	"os"
	"runtime"
	"time"

	"obs-pktm/internal/actions"
	"obs-pktm/internal/config"
	"obs-pktm/internal/hotkeys"
	"obs-pktm/internal/logging"
	"obs-pktm/internal/obsws"
	"obs-pktm/internal/timer"
	"obs-pktm/internal/winmsg"
)

var appLogger *logging.Logger

func main() {
	runtime.LockOSThread()

	cfg, err := config.Load("config.json")
	if err != nil {
		fatal(err)
	}

	logger, err := logging.NewLastSession("logs")
	if err != nil {
		fatal(err)
	}
	appLogger = logger
	defer logger.Close()
	for _, warning := range cfg.CompatibilityWarnings() {
		logger.Error("config_compatibility_warning", warning, nil)
		fmt.Fprintln(os.Stderr, warning)
	}

	countdown, err := timer.NewOverlay(timer.Options{
		Duration: cfg.TimerDuration(),
	})
	if err != nil {
		fatal(err)
	}
	defer countdown.Close()

	obs := obsws.Client{
		URL:      cfg.OBSWebSocket.URL,
		Password: cfg.OBSWebSocket.Password,
		Timeout:  cfg.OBSRequestTimeout(),
	}
	actionLibrary := actions.New(obs, countdown, winmsg.ErrorBox)
	actionLibrary.SetLogger(logger)
	renameRules := cfg.RecordingRenameRules()
	actionLibrary.SetRenameRules(actions.RenameRules{
		Prefix:              renameRules.Prefix,
		MinDuration:         time.Duration(renameRules.MinDurationSeconds) * time.Second,
		MinSize:             renameRules.MinSizeBytes(),
		Directory:           renameRules.Directory,
		ManualShortDuration: time.Duration(renameRules.ManualShortDurationSeconds) * time.Second,
		ManualShortDir:      renameRules.ManualShortDirectory,
	})
	checkRules := cfg.RecordingCheckRules()
	actionLibrary.SetStartupCheckRules(actions.StartupCheckRules{
		Delay:   time.Duration(checkRules.StartupDelaySeconds) * time.Second,
		Timeout: time.Duration(checkRules.StartupTimeoutSeconds) * time.Second,
	})
	countdown.SetOnFinish(actionLibrary.RestartRecordingAndResetTimer)

	listener, err := hotkeys.NewListener(hotkeys.Actions{
		F9:  actionLibrary.StartRecording,
		F10: actionLibrary.StopRecordingAndResetTimer,
		F12: winmsg.Quit,
	})
	if err != nil {
		fatal(err)
	}
	defer listener.Close()

	fmt.Println("obs-pktm active: F9 starts recording/timer, F10 stops recording/timer, F12 exits.")
	if err := winmsg.Run(); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	message := err.Error()
	if appLogger != nil {
		appLogger.Error("fatal", message, nil)
	}
	winmsg.ErrorBox("obs-pktm", message)
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
