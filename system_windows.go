package main

import (
	"fmt"
	"math"
	"runtime"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"github.com/atotto/clipboard"
	"golang.org/x/sys/windows"
)

// The Windows side of the remote: Core Audio for volume, media keys for the
// player, SendInput for text and the Win32 clipboard.

const mediaMissing = "Media keys are not available."

// audioOutputsAvailable says whether the PC can list and switch sound
// outputs. Windows has no public API for switching, so the list stays empty.
const audioOutputsAvailable = false

// platformFeatures: only volume, media keys, input, typing and the clipboard work on
// Windows so far. Each port step turns its feature on here.
func platformFeatures() featureSet {
	return featureSet{Outputs: audioOutputsAvailable}
}

const (
	vkReturn         = 0x0D
	vkTab            = 0x09
	vkMediaNextTrack = 0xB0
	vkMediaPrevTrack = 0xB1
	vkMediaPlayPause = 0xB3
)

var mediaKeys = map[string]uint16{
	"previous": vkMediaPrevTrack,
	"toggle":   vkMediaPlayPause,
	"next":     vkMediaNextTrack,
}

// Media keys reach whichever player Windows routes them to, so there is
// always something to press. What is playing isn't read yet.
func mediaAvailable() bool { return true }
func nowPlaying() *track   { return nil }

func mediaAction(action string) error {
	vk := mediaKeys[action]
	return sendKeys([]keyInput{virtualKey(vk, false), virtualKey(vk, true)})
}

// typeText types each character as Unicode input, so it doesn't depend on the
// keyboard layout. Line breaks and tabs press the real keys, which apps
// expect instead of the raw characters.
func typeText(text string) error {
	var inputs []keyInput
	for _, r := range strings.ReplaceAll(text, "\r\n", "\n") {
		switch r {
		case '\n':
			inputs = append(inputs, virtualKey(vkReturn, false), virtualKey(vkReturn, true))
			continue
		case '\t':
			inputs = append(inputs, virtualKey(vkTab, false), virtualKey(vkTab, true))
			continue
		}
		for _, unit := range utf16.Encode([]rune{r}) {
			for _, up := range []bool{false, true} {
				var k keyInput
				k.kind = inputKeyboard
				k.ki.scan = unit
				k.ki.flags = keyeventfUnicode
				if up {
					k.ki.flags |= keyeventfKeyUp
				}
				inputs = append(inputs, k)
			}
		}
	}
	return sendKeys(inputs)
}

func pressEnter() error {
	return sendKeys([]keyInput{virtualKey(vkReturn, false), virtualKey(vkReturn, true)})
}

// readClipboard returns "" when nothing is copied or the clipboard holds no text.
func readClipboard() string {
	text, err := clipboard.ReadAll()
	if err != nil {
		return ""
	}
	return strings.ReplaceAll(text, "\r\n", "\n")
}

// writeClipboard uses Windows line breaks, which Notepad and older apps need.
func writeClipboard(text string) error {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return clipboard.WriteAll(strings.ReplaceAll(text, "\n", "\r\n"))
}

// Core Audio, called through COM vtables so no cgo is needed.

var (
	procCoInitializeEx   = windows.NewLazySystemDLL("ole32.dll").NewProc("CoInitializeEx")
	procCoCreateInstance = windows.NewLazySystemDLL("ole32.dll").NewProc("CoCreateInstance")

	clsidMMDeviceEnumerator = windows.GUID{Data1: 0xBCDE0395, Data2: 0xE52F, Data3: 0x467C, Data4: [8]byte{0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E}}
	iidIMMDeviceEnumerator  = windows.GUID{Data1: 0xA95664D2, Data2: 0x9614, Data3: 0x4F35, Data4: [8]byte{0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6}}
	iidIAudioEndpointVolume = windows.GUID{Data1: 0x5CDF2C82, Data2: 0x841E, Data3: 0x4546, Data4: [8]byte{0x97, 0x22, 0x0C, 0xF7, 0x40, 0x78, 0x22, 0x9A}}
)

const (
	clsctxAll = 0x17
	eRender   = 0
	eConsole  = 0
	eNotFound = 0x80070490 // HRESULT_FROM_WIN32(ERROR_NOT_FOUND): no output device

	// Vtable slots, counting IUnknown's three.
	slotRelease                    = 2
	slotGetDefaultAudioEndpoint    = 4  // IMMDeviceEnumerator
	slotActivate                   = 3  // IMMDevice
	slotSetMasterVolumeLevelScalar = 7  // IAudioEndpointVolume
	slotGetMasterVolumeLevelScalar = 9  // IAudioEndpointVolume
	slotSetMute                    = 14 // IAudioEndpointVolume
	slotGetMute                    = 15 // IAudioEndpointVolume
)

// comObject is a COM interface pointer: its first word is the vtable.
type comObject struct {
	vtbl *[32]uintptr
}

func (o *comObject) method(slot int) uintptr { return o.vtbl[slot] }

func (o *comObject) release() {
	syscall.SyscallN(o.method(slotRelease), uintptr(unsafe.Pointer(o)))
}

func hresult(hr uintptr) error {
	if int32(hr) < 0 {
		return fmt.Errorf("Core Audio error 0x%08X", uint32(hr))
	}
	return nil
}

// withSpeakers runs fn with the volume control of the default output.
func withSpeakers(fn func(volume *comObject) error) error {
	// COM is set up per thread, so stay on this one until done.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if hr, _, _ := procCoInitializeEx.Call(0, windows.COINIT_MULTITHREADED); int32(hr) >= 0 {
		defer windows.CoUninitialize()
	}

	var enumerator *comObject
	hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)), 0, clsctxAll,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)), uintptr(unsafe.Pointer(&enumerator)))
	if err := hresult(hr); err != nil {
		return err
	}
	defer enumerator.release()

	var device *comObject
	hr, _, _ = syscall.SyscallN(enumerator.method(slotGetDefaultAudioEndpoint),
		uintptr(unsafe.Pointer(enumerator)), eRender, eConsole, uintptr(unsafe.Pointer(&device)))
	if uint32(hr) == eNotFound || (hr == 0 && device == nil) {
		return errNoSpeakers
	}
	if err := hresult(hr); err != nil {
		return err
	}
	defer device.release()

	var volume *comObject
	hr, _, _ = syscall.SyscallN(device.method(slotActivate),
		uintptr(unsafe.Pointer(device)), uintptr(unsafe.Pointer(&iidIAudioEndpointVolume)), clsctxAll, 0,
		uintptr(unsafe.Pointer(&volume)))
	if err := hresult(hr); err != nil {
		return err
	}
	defer volume.release()
	return fn(volume)
}

func readVolume() (volume int, muted bool, err error) {
	err = withSpeakers(func(v *comObject) error {
		var level float32
		hr, _, _ := syscall.SyscallN(v.method(slotGetMasterVolumeLevelScalar),
			uintptr(unsafe.Pointer(v)), uintptr(unsafe.Pointer(&level)))
		if err := hresult(hr); err != nil {
			return err
		}
		var mute int32
		hr, _, _ = syscall.SyscallN(v.method(slotGetMute),
			uintptr(unsafe.Pointer(v)), uintptr(unsafe.Pointer(&mute)))
		if err := hresult(hr); err != nil {
			return err
		}
		volume, muted = int(math.Round(float64(level)*100)), mute != 0
		return nil
	})
	return volume, muted, err
}

// setVolume also unmutes, so the new level is heard.
func setVolume(volume int) error {
	return withSpeakers(func(v *comObject) error {
		// The level is a float. On amd64 Go passes the first four arguments in
		// both the integer and the XMM registers, so its bits arrive in XMM1.
		level := math.Float32bits(float32(volume) / 100)
		hr, _, _ := syscall.SyscallN(v.method(slotSetMasterVolumeLevelScalar),
			uintptr(unsafe.Pointer(v)), uintptr(level), 0)
		if err := hresult(hr); err != nil {
			return err
		}
		hr, _, _ = syscall.SyscallN(v.method(slotSetMute), uintptr(unsafe.Pointer(v)), 0, 0)
		return hresult(hr)
	})
}

func setMuted(muted bool) error {
	return withSpeakers(func(v *comObject) error {
		var value uintptr
		if muted {
			value = 1
		}
		hr, _, _ := syscall.SyscallN(v.method(slotSetMute), uintptr(unsafe.Pointer(v)), value, 0)
		return hresult(hr)
	})
}
