//go:build windows

package timer

import (
	"fmt"
	"syscall"
	"time"
	"unsafe"

	"obs-pktm/internal/winmsg"
)

const (
	wmPaint = 0x000F
	wmTimer = 0x0113

	wsPopup = 0x80000000

	wsExTopmost    = 0x00000008
	wsExToolWindow = 0x00000080
	wsExLayered    = 0x00080000
	wsExNoActivate = 0x08000000

	swpNoActivate = 0x0010
	swpShowWindow = 0x0040

	swHide = 0

	lwaAlpha = 0x00000002

	smCxScreen = 0
	smCyScreen = 1

	colorWindow = 5

	timerID = 1

	fwBold           = 700
	defaultCharset   = 1
	defaultPitch     = 0
	cleartypeQuality = 5

	transparent = 1

	dtCenter     = 0x00000001
	dtVCenter    = 0x00000004
	dtSingleLine = 0x00000020
)

var (
	user32 = syscall.NewLazyDLL("user32.dll")
	gdi32  = syscall.NewLazyDLL("gdi32.dll")

	procRegisterClassExW      = user32.NewProc("RegisterClassExW")
	procCreateWindowExW       = user32.NewProc("CreateWindowExW")
	procDefWindowProcW        = user32.NewProc("DefWindowProcW")
	procSetWindowPos          = user32.NewProc("SetWindowPos")
	procShowWindow            = user32.NewProc("ShowWindow")
	procSetLayeredWindowAttrs = user32.NewProc("SetLayeredWindowAttributes")
	procGetSystemMetrics      = user32.NewProc("GetSystemMetrics")
	procBeginPaint            = user32.NewProc("BeginPaint")
	procEndPaint              = user32.NewProc("EndPaint")
	procInvalidateRect        = user32.NewProc("InvalidateRect")
	procSetTimer              = user32.NewProc("SetTimer")
	procKillTimer             = user32.NewProc("KillTimer")
	procCreateSolidBrush      = gdi32.NewProc("CreateSolidBrush")
	procDeleteObject          = gdi32.NewProc("DeleteObject")
	procFillRect              = user32.NewProc("FillRect")
	procSetBkMode             = gdi32.NewProc("SetBkMode")
	procSetTextColor          = gdi32.NewProc("SetTextColor")
	procSelectObject          = gdi32.NewProc("SelectObject")
	procDrawTextW             = user32.NewProc("DrawTextW")
	procCreateFontW           = gdi32.NewProc("CreateFontW")

	windowProcCallback = syscall.NewCallback(windowProc)
	activeOverlay      *Overlay
)

type Options struct {
	Duration time.Duration
}

type Overlay struct {
	hInstance uintptr
	hwnd      uintptr

	duration    time.Duration
	secondsLeft int
	running     bool
	finished    bool

	smallFont uintptr
	largeFont uintptr
}

type rect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type paintStruct struct {
	Hdc         uintptr
	Erase       int32
	Paint       rect
	Restore     int32
	IncUpdate   int32
	RGBReserved [32]byte
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

func NewOverlay(options Options) (*Overlay, error) {
	if options.Duration <= 0 {
		options.Duration = 10 * time.Minute
	}

	instance, err := winmsg.ModuleHandle()
	if err != nil {
		return nil, err
	}

	overlay := &Overlay{
		hInstance: instance,
		duration:  options.Duration,
	}

	if err := overlay.initWindow(); err != nil {
		return nil, err
	}

	activeOverlay = overlay
	return overlay, nil
}

func (o *Overlay) Start() {
	o.secondsLeft = int(o.duration.Seconds())
	o.running = true
	o.finished = false

	screenW := int32(systemMetric(smCxScreen))
	x := (screenW - 200) / 2

	procKillTimer.Call(o.hwnd, timerID)
	procSetLayeredWindowAttrs.Call(o.hwnd, 0, 128, lwaAlpha)
	procSetWindowPos.Call(o.hwnd, ^uintptr(0), uintptr(x), 20, 200, 50, swpNoActivate|swpShowWindow)
	procInvalidateRect.Call(o.hwnd, 0, 1)
	procSetTimer.Call(o.hwnd, timerID, 1000, 0)
}

func (o *Overlay) Stop() {
	o.running = false
	o.finished = true
	procKillTimer.Call(o.hwnd, timerID)
	procShowWindow.Call(o.hwnd, swHide)
}

func (o *Overlay) Close() {
	if o == nil {
		return
	}

	if o.hwnd != 0 {
		procKillTimer.Call(o.hwnd, timerID)
	}
	if o.smallFont != 0 {
		procDeleteObject.Call(o.smallFont)
		o.smallFont = 0
	}
	if o.largeFont != 0 {
		procDeleteObject.Call(o.largeFont)
		o.largeFont = 0
	}
	if activeOverlay == o {
		activeOverlay = nil
	}
}

func (o *Overlay) initWindow() error {
	className := syscall.StringToUTF16Ptr("ObsPktmOverlayWindow")
	wc := wndClassEx{
		Size:       uint32(unsafe.Sizeof(wndClassEx{})),
		WndProc:    windowProcCallback,
		Instance:   o.hInstance,
		Background: colorWindow + 1,
		ClassName:  className,
	}

	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return fmt.Errorf("RegisterClassExW failed: %w", err)
	}

	hwnd, _, err := procCreateWindowExW.Call(
		wsExTopmost|wsExToolWindow|wsExLayered|wsExNoActivate,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("OBS PKTM Timer"))),
		wsPopup,
		0, 0, 200, 50,
		0, 0, o.hInstance, 0,
	)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowExW failed: %w", err)
	}

	o.hwnd = hwnd
	o.smallFont = createFont(-28)
	o.largeFont = createFont(-240)
	return nil
}

func (o *Overlay) tick() {
	if !o.running || o.finished {
		return
	}

	o.secondsLeft--
	if o.secondsLeft <= 0 {
		o.finish()
		return
	}

	procInvalidateRect.Call(o.hwnd, 0, 1)
}

func (o *Overlay) finish() {
	o.secondsLeft = 0
	o.running = false
	o.finished = true

	screenW := systemMetric(smCxScreen)
	screenH := systemMetric(smCyScreen)

	procKillTimer.Call(o.hwnd, timerID)
	procSetLayeredWindowAttrs.Call(o.hwnd, 0, 255, lwaAlpha)
	procSetWindowPos.Call(o.hwnd, ^uintptr(0), 0, 0, uintptr(screenW), uintptr(screenH), swpNoActivate|swpShowWindow)
	procInvalidateRect.Call(o.hwnd, 0, 1)
}

func (o *Overlay) paint() {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(o.hwnd, uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	defer procEndPaint.Call(o.hwnd, uintptr(unsafe.Pointer(&ps)))

	bounds := rect{Right: 200, Bottom: 50}
	if o.finished {
		bounds = rect{Right: int32(systemMetric(smCxScreen)), Bottom: int32(systemMetric(smCyScreen))}
	}

	brush, _, _ := procCreateSolidBrush.Call(rgb(0x20, 0x20, 0x20))
	if brush != 0 {
		procFillRect.Call(hdc, uintptr(unsafe.Pointer(&bounds)), brush)
		procDeleteObject.Call(brush)
	}

	text := o.displayText()
	textPtr := syscall.StringToUTF16Ptr(text)

	procSetBkMode.Call(hdc, transparent)
	procSetTextColor.Call(hdc, rgb(255, 255, 255))

	font := o.smallFont
	if o.finished {
		font = o.largeFont
	}
	if font != 0 {
		old, _, _ := procSelectObject.Call(hdc, font)
		defer procSelectObject.Call(hdc, old)
	}

	procDrawTextW.Call(
		hdc,
		uintptr(unsafe.Pointer(textPtr)),
		uintptr(len(syscall.StringToUTF16(text))-1),
		uintptr(unsafe.Pointer(&bounds)),
		dtCenter|dtVCenter|dtSingleLine,
	)
}

func (o *Overlay) displayText() string {
	if o.finished {
		return "FIN"
	}

	mins := o.secondsLeft / 60
	secs := o.secondsLeft % 60
	return fmt.Sprintf("%02d:%02d", mins, secs)
}

func windowProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmPaint:
		if activeOverlay != nil {
			activeOverlay.paint()
			return 0
		}
	case wmTimer:
		if activeOverlay != nil && wParam == timerID {
			activeOverlay.tick()
			return 0
		}
	}

	r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
	return r
}

func createFont(height int32) uintptr {
	face := syscall.StringToUTF16Ptr("Segoe UI")
	font, _, _ := procCreateFontW.Call(
		uintptr(height),
		0,
		0,
		0,
		fwBold,
		0,
		0,
		0,
		defaultCharset,
		0,
		0,
		cleartypeQuality,
		defaultPitch,
		uintptr(unsafe.Pointer(face)),
	)
	return font
}

func systemMetric(index int32) int32 {
	r, _, _ := procGetSystemMetrics.Call(uintptr(index))
	return int32(r)
}

func rgb(r, g, b byte) uintptr {
	return uintptr(uint32(r) | uint32(g)<<8 | uint32(b)<<16)
}
