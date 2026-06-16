//go:build windows

package hotkeys

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"obs-pktm/internal/winmsg"
)

const (
	whKeyboardLL = 13

	wmKeyDown    = 0x0100
	wmKeyUp      = 0x0101
	wmSysKeyDown = 0x0104
	wmSysKeyUp   = 0x0105

	vkF9  = 0x78
	vkF10 = 0x79
	vkF12 = 0x7B
)

var (
	user32 = syscall.NewLazyDLL("user32.dll")

	procSetWindowsHookExW   = user32.NewProc("SetWindowsHookExW")
	procCallNextHookEx      = user32.NewProc("CallNextHookEx")
	procUnhookWindowsHookEx = user32.NewProc("UnhookWindowsHookEx")

	keyboardProcCallback = syscall.NewCallback(keyboardProc)
	activeListener       *Listener
)

type Actions struct {
	F9  func()
	F10 func()
	F12 func()
}

type Listener struct {
	hook     uintptr
	keysDown map[uint32]bool
	actions  Actions
}

type keyboardHookStruct struct {
	VkCode      uint32
	ScanCode    uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

func NewListener(actions Actions) (*Listener, error) {
	if activeListener != nil {
		return nil, errors.New("hotkey listener already installed")
	}

	instance, err := winmsg.ModuleHandle()
	if err != nil {
		return nil, err
	}

	listener := &Listener{
		keysDown: make(map[uint32]bool),
		actions:  actions,
	}

	hook, _, callErr := procSetWindowsHookExW.Call(whKeyboardLL, keyboardProcCallback, instance, 0)
	if hook == 0 {
		return nil, fmt.Errorf("SetWindowsHookExW failed: %w", callErr)
	}

	listener.hook = hook
	activeListener = listener
	return listener, nil
}

func (l *Listener) Close() {
	if l == nil || l.hook == 0 {
		return
	}

	procUnhookWindowsHookEx.Call(l.hook)
	l.hook = 0
	if activeListener == l {
		activeListener = nil
	}
}

func keyboardProc(nCode int, wParam, lParam uintptr) uintptr {
	if nCode >= 0 && activeListener != nil {
		info := (*keyboardHookStruct)(unsafe.Pointer(lParam))
		activeListener.handleKey(wParam, info.VkCode)
	}

	r, _, _ := procCallNextHookEx.Call(0, uintptr(nCode), wParam, lParam)
	return r
}

func (l *Listener) handleKey(message uintptr, vkCode uint32) {
	switch message {
	case wmKeyDown, wmSysKeyDown:
		if l.keysDown[vkCode] {
			return
		}
		l.keysDown[vkCode] = true
		l.runAction(vkCode)
	case wmKeyUp, wmSysKeyUp:
		l.keysDown[vkCode] = false
	}
}

func (l *Listener) runAction(vkCode uint32) {
	var action func()

	switch vkCode {
	case vkF9:
		action = l.actions.F9
	case vkF10:
		action = l.actions.F10
	case vkF12:
		action = l.actions.F12
	}

	if action != nil {
		action()
	}
}
