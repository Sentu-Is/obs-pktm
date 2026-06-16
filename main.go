//go:build windows

package main

import (
	"fmt"
	"os"
	"runtime"
	"time"

	"obs-pktm/internal/hotkeys"
	"obs-pktm/internal/timer"
	"obs-pktm/internal/winmsg"
)

func main() {
	runtime.LockOSThread()

	countdown, err := timer.NewOverlay(timer.Options{
		Duration: 10 * time.Minute,
	})
	if err != nil {
		fatal(err)
	}
	defer countdown.Close()

	listener, err := hotkeys.NewListener(hotkeys.Actions{
		F9:  countdown.Start,
		F10: countdown.Stop,
		F12: winmsg.Quit,
	})
	if err != nil {
		fatal(err)
	}
	defer listener.Close()

	fmt.Println("obs-pktm activo: F9 inicia el timer, F10 lo detiene, F12 sale.")
	if err := winmsg.Run(); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	message := err.Error()
	winmsg.ErrorBox("obs-pktm", message)
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
