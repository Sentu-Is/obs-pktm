//go:build windows

package timer

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

const (
	fileMapAllAccess = 0x000F001F

	rtssSignature       = uint32('R')<<24 | uint32('T')<<16 | uint32('S')<<8 | uint32('S')
	rtssMinVersion      = 0x00020000
	rtssExtendedVersion = 0x00020007
	rtssBusyVersion     = 0x0002000e

	rtssOwner        = "obs-pktm"
	rtssSharedMemory = "RTSSSharedMemoryV2"

	rtssOSDTextSize     = 256
	rtssOSDOwnerOffset  = 256
	rtssOSDOwnerSize    = 256
	rtssOSDExOffset     = 512
	rtssOSDExSize       = 4096
	rtssFirstClientSlot = 1
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procOpenFileMappingW = kernel32.NewProc("OpenFileMappingW")
	procMapViewOfFile    = kernel32.NewProc("MapViewOfFile")
	procUnmapViewOfFile  = kernel32.NewProc("UnmapViewOfFile")
	procCloseHandle      = kernel32.NewProc("CloseHandle")
)

type rtssOSD struct {
	owner    string
	slot     uint32
	mu       sync.Mutex
	released bool
}

type rtssSharedMemoryHeader struct {
	Signature                    uint32
	Version                      uint32
	AppEntrySize                 uint32
	AppArrOffset                 uint32
	AppArrSize                   uint32
	OSDEntrySize                 uint32
	OSDArrOffset                 uint32
	OSDArrSize                   uint32
	OSDFrame                     uint32
	Busy                         int32
	DesktopVideoCaptureFlags     uint32
	DesktopVideoCaptureStat      [5]uint32
	LastForegroundApp            uint32
	LastForegroundAppProcessID   uint32
	ProcessPerfCountersEntrySize uint32
	ProcessPerfCountersArrOffset uint32
}

type rtssMappedMemory struct {
	handle uintptr
	base   uintptr
	mem    *rtssSharedMemoryHeader
}

func newRTSSOSD(owner string) (*rtssOSD, error) {
	if owner == "" {
		return nil, errors.New("RTSS OSD owner is empty")
	}
	if len(owner) >= rtssOSDOwnerSize {
		return nil, fmt.Errorf("RTSS OSD owner exceeds %d bytes", rtssOSDOwnerSize-1)
	}

	return &rtssOSD{owner: owner}, nil
}

func (o *rtssOSD) Update(text string) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.released {
		return nil
	}
	if len(text) >= rtssOSDExSize {
		return fmt.Errorf("RTSS OSD text exceeds %d bytes", rtssOSDExSize-1)
	}

	mapped, err := openRTSSSharedMemory()
	if err != nil {
		return err
	}
	defer mapped.close()

	entry, err := o.captureEntry(mapped)
	if err != nil {
		return err
	}

	unlock, locked := mapped.lockOSD()
	if mapped.mem.Version >= rtssBusyVersion && !locked {
		return nil
	}
	if unlock != nil {
		defer unlock()
	}

	if mapped.mem.Version >= rtssExtendedVersion && mapped.mem.OSDEntrySize >= rtssOSDExOffset+rtssOSDExSize {
		writeCString(entry.osdEx(), text)
	} else {
		writeCString(entry.osd(), text)
	}

	atomic.AddUint32(&mapped.mem.OSDFrame, 1)
	return nil
}

func (o *rtssOSD) Release() {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.released {
		return
	}
	o.released = true

	mapped, err := openRTSSSharedMemory()
	if err != nil {
		return
	}
	defer mapped.close()

	unlock, locked := mapped.lockOSD()
	if mapped.mem.Version >= rtssBusyVersion && !locked {
		return
	}
	if unlock != nil {
		defer unlock()
	}

	for i := uint32(rtssFirstClientSlot); i < mapped.mem.OSDArrSize; i++ {
		entry := mapped.osdEntry(i)
		if entry.ownerMatches(o.owner) {
			clearBytes(entry.raw())
			atomic.AddUint32(&mapped.mem.OSDFrame, 1)
		}
	}
	o.slot = 0
}

func (o *rtssOSD) captureEntry(mapped *rtssMappedMemory) (*rtssOSDEntry, error) {
	if o.slot >= rtssFirstClientSlot && o.slot < mapped.mem.OSDArrSize {
		entry := mapped.osdEntry(o.slot)
		if entry.ownerMatches(o.owner) {
			return entry, nil
		}
		o.slot = 0
	}

	for pass := 0; pass < 2; pass++ {
		for i := uint32(rtssFirstClientSlot); i < mapped.mem.OSDArrSize; i++ {
			entry := mapped.osdEntry(i)

			if pass == 1 && entry.ownerEmpty() {
				writeCString(entry.owner(), o.owner)
			}

			if entry.ownerMatches(o.owner) {
				o.slot = i
				return entry, nil
			}
		}
	}

	return nil, errors.New("no free RTSS OSD slot is available")
}

type rtssOSDEntry struct {
	base uintptr
	size uint32
}

func (e *rtssOSDEntry) raw() []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(e.base)), int(e.size))
}

func (e *rtssOSDEntry) osd() []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(e.base)), rtssOSDTextSize)
}

func (e *rtssOSDEntry) owner() []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(e.base+rtssOSDOwnerOffset)), rtssOSDOwnerSize)
}

func (e *rtssOSDEntry) osdEx() []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(e.base+rtssOSDExOffset)), rtssOSDExSize)
}

func (e *rtssOSDEntry) ownerEmpty() bool {
	return cstringLen(e.owner()) == 0
}

func (e *rtssOSDEntry) ownerMatches(owner string) bool {
	return cstringEquals(e.owner(), owner)
}

func openRTSSSharedMemory() (*rtssMappedMemory, error) {
	name := syscall.StringToUTF16Ptr(rtssSharedMemory)
	handle, _, err := procOpenFileMappingW.Call(fileMapAllAccess, 0, uintptr(unsafe.Pointer(name)))
	if handle == 0 {
		return nil, fmt.Errorf("RTSS shared memory is not available: %w", nonzeroErr(err))
	}

	base, _, err := procMapViewOfFile.Call(handle, fileMapAllAccess, 0, 0, 0)
	if base == 0 {
		procCloseHandle.Call(handle)
		return nil, fmt.Errorf("MapViewOfFile failed for RTSS shared memory: %w", nonzeroErr(err))
	}

	mapped := &rtssMappedMemory{
		handle: handle,
		base:   base,
		mem:    (*rtssSharedMemoryHeader)(unsafe.Pointer(base)),
	}
	if err := mapped.validate(); err != nil {
		mapped.close()
		return nil, err
	}

	return mapped, nil
}

func (m *rtssMappedMemory) validate() error {
	if m.mem.Signature != rtssSignature || m.mem.Version < rtssMinVersion {
		return fmt.Errorf("invalid RTSS shared memory signature or version: signature=0x%08x version=0x%08x", m.mem.Signature, m.mem.Version)
	}
	if m.mem.OSDArrOffset == 0 || m.mem.OSDArrSize <= rtssFirstClientSlot {
		return errors.New("RTSS shared memory has no usable OSD slots")
	}
	if m.mem.OSDEntrySize < rtssOSDOwnerOffset+rtssOSDOwnerSize {
		return errors.New("RTSS OSD entry size is too small")
	}
	return nil
}

func (m *rtssMappedMemory) osdEntry(index uint32) *rtssOSDEntry {
	base := m.base + uintptr(m.mem.OSDArrOffset) + uintptr(index)*uintptr(m.mem.OSDEntrySize)
	return &rtssOSDEntry{base: base, size: m.mem.OSDEntrySize}
}

func (m *rtssMappedMemory) lockOSD() (func(), bool) {
	if m.mem.Version < rtssBusyVersion {
		return nil, true
	}
	if !atomic.CompareAndSwapInt32(&m.mem.Busy, 0, 1) {
		return nil, false
	}
	return func() {
		atomic.StoreInt32(&m.mem.Busy, 0)
	}, true
}

func (m *rtssMappedMemory) close() {
	if m.base != 0 {
		procUnmapViewOfFile.Call(m.base)
		m.base = 0
	}
	if m.handle != 0 {
		procCloseHandle.Call(m.handle)
		m.handle = 0
	}
}

func writeCString(dst []byte, value string) {
	clearBytes(dst)
	if len(dst) == 0 {
		return
	}
	if len(value) >= len(dst) {
		value = value[:len(dst)-1]
	}
	copy(dst, value)
}

func clearBytes(dst []byte) {
	for i := range dst {
		dst[i] = 0
	}
}

func cstringEquals(src []byte, value string) bool {
	if len(value) >= len(src) {
		return false
	}
	n := cstringLen(src)
	if n != len(value) {
		return false
	}
	for i := 0; i < n; i++ {
		if src[i] != value[i] {
			return false
		}
	}
	return true
}

func cstringLen(src []byte) int {
	for i, b := range src {
		if b == 0 {
			return i
		}
	}
	return len(src)
}

func nonzeroErr(err error) error {
	if err == syscall.Errno(0) {
		return syscall.EINVAL
	}
	return err
}
