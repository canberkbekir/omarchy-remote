package main

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/atotto/clipboard"
	"golang.org/x/sys/windows"
)

// The Windows side of the remote: Core Audio for volume, media keys for the
// player, SendInput for text and the Win32 clipboard.

const mediaMissing = "Media keys are not available."

// platformFeatures: only volume, media keys, input, typing and the clipboard work on
// Windows so far. Each port step turns its feature on here.
func platformFeatures() featureSet {
	return featureSet{Power: true, Screens: true, Brightness: brightnessAvailable(), Outputs: outputsAvailable(), NowPlaying: nowPlayingAvailable(), Party: partyAvailable}
}

// partyAvailable: guests need the game controller, which Windows lacks so far.
const partyAvailable = false

var (
	procLockWorkStation    = windows.NewLazySystemDLL("user32.dll").NewProc("LockWorkStation")
	procSendMessageTimeout = windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")
)

const (
	hwndBroadcast    = 0xFFFF
	wmSyscommand     = 0x0112
	scMonitorPower   = 0xF170
	monitorPowerOff  = 2
	smtoAbortIfHung  = 0x0002
	screensTimeoutMs = 1000
)

// shutdown.exe is started without waiting, because it ends this process.
var shutdownCommands = map[string][]string{
	"reboot":   {"shutdown", "/r", "/t", "0"},
	"shutdown": {"shutdown", "/s", "/t", "0"},
}

func powerAction(action string) error {
	if action == "lock" {
		if ok, _, err := procLockWorkStation.Call(); ok == 0 {
			return err
		}
		return nil
	}
	argv, ok := shutdownCommands[action]
	if !ok {
		return errUnknownAction
	}
	return startDetached(argv...)
}

// setScreens turns the monitors off with SC_MONITORPOWER. Windows has no
// matching "on" message that works reliably, so a tiny pointer nudge wakes
// them instead, the same way a real mouse would.
func setScreens(on bool) error {
	if on {
		var m mouseInput
		m.mi.flags = mouseeventfMove
		m.mi.dx = 1
		if err := sendMouse(m); err != nil {
			return err
		}
		m.mi.dx = -1
		return sendMouse(m)
	}
	// A broadcast reports a timeout when any one window is hung, though the
	// others already turned the screens off, so a timeout still counts.
	var result uintptr
	ok, _, err := procSendMessageTimeout.Call(hwndBroadcast, wmSyscommand, scMonitorPower, monitorPowerOff,
		smtoAbortIfHung, screensTimeoutMs, uintptr(unsafe.Pointer(&result)))
	if ok == 0 && err != windows.ERROR_SUCCESS && err != windows.ERROR_TIMEOUT {
		return err
	}
	return nil
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
// always something to press.
func mediaAvailable() bool { return true }

// The player that Windows shows in its media flyout is the current session
// of the GlobalSystemMediaTransportControlsSessionManager. Players publish
// their title, artist, cover, state and timeline there.

const (
	// IGlobalSystemMediaTransportControlsSessionManagerStatics
	slotRequestAsync = 6
	// IGlobalSystemMediaTransportControlsSessionManager
	slotGetCurrentSession = 6
	// IGlobalSystemMediaTransportControlsSession
	slotSourceAppUserModelID       = 6
	slotTryGetMediaPropertiesAsync = 7
	slotGetTimelineProperties      = 8
	slotGetPlaybackInfo            = 9
	slotTrySkipNextAsync           = 16
	slotTrySkipPreviousAsync       = 17
	slotTryTogglePlayPauseAsync    = 20
	// IGlobalSystemMediaTransportControlsSessionMediaProperties
	slotTitle     = 6
	slotArtist    = 9
	slotThumbnail = 15
	// IGlobalSystemMediaTransportControlsSessionPlaybackInfo
	slotPlaybackStatus = 7
	// IGlobalSystemMediaTransportControlsSessionTimelineProperties
	slotStartTime       = 6
	slotEndTime         = 7
	slotTimelinePos     = 10
	slotLastUpdatedTime = 11

	// GlobalSystemMediaTransportControlsSessionPlaybackStatus
	playbackClosed  = 0
	playbackStopped = 3
	playbackPlaying = 4

	ticksPerSecond = 1e7                // TimeSpan and DateTime count 100 ns
	ticksTo1970    = 116444736000000000 // DateTime starts at 1601
)

var errNoSession = errors.New("no media session")

// sessionMu lets one session call run at a time, so a hung player holds at
// most one OS thread.
var sessionMu sync.Mutex

// withSession runs fn on the current media session.
func withSession(fn func(session *comObject) error) error {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	return withWinRT(func() error {
		statics, err := activationFactory(classSessionManager, &iidSessionManagerStatics)
		if err != nil {
			return err
		}
		defer statics.release()
		var manager *comObject
		if err := callAsync(statics, slotRequestAsync, &iidAsyncOperationSessionManager, unsafe.Pointer(&manager)); err != nil {
			return err
		}
		if manager == nil {
			return errNoSession
		}
		defer manager.release()
		session, err := getObject(manager, slotGetCurrentSession)
		if err != nil {
			return err
		}
		if session == nil {
			return errNoSession
		}
		defer session.release()
		return fn(session)
	})
}

func mediaProperties(session *comObject) (*comObject, error) {
	var props *comObject
	if err := callAsync(session, slotTryGetMediaPropertiesAsync, &iidAsyncOperationMediaProperties, unsafe.Pointer(&props)); err != nil {
		return nil, err
	}
	if props == nil {
		return nil, errNoSession
	}
	return props, nil
}

// nowPlayingAvailable reports whether this Windows has the media session API,
// which arrived in Windows 10 1809. That can't change while running.
var nowPlayingAvailable = sync.OnceValue(func() bool {
	return withWinRT(func() error {
		statics, err := activationFactory(classSessionManager, &iidSessionManagerStatics)
		if err == nil {
			statics.release()
		}
		return err
	}) == nil
})

func nowPlaying() *track {
	var t *track
	withSession(func(session *comObject) error {
		props, err := mediaProperties(session)
		if err != nil {
			return err
		}
		defer props.release()
		title, artist := getString(props, slotTitle), getString(props, slotArtist)
		thumbnail, _ := getObject(props, slotThumbnail)
		hasThumbnail := thumbnail != nil
		if hasThumbnail {
			thumbnail.release()
		}

		status := int32(playbackClosed)
		if info, err := getObject(session, slotGetPlaybackInfo); err == nil && info != nil {
			syscall.SyscallN(info.method(slotPlaybackStatus), uintptr(unsafe.Pointer(info)), uintptr(unsafe.Pointer(&status)))
			info.release()
		}
		if title == "" || status == playbackClosed || status == playbackStopped {
			return nil
		}
		t = &track{
			Title:   title,
			Artist:  artist,
			Player:  playerName(getString(session, slotSourceAppUserModelID)),
			Playing: status == playbackPlaying,
		}
		if timeline, err := getObject(session, slotGetTimelineProperties); err == nil && timeline != nil {
			var start, end, position, updated int64
			for _, field := range []struct {
				slot  int
				value *int64
			}{{slotStartTime, &start}, {slotEndTime, &end}, {slotTimelinePos, &position}, {slotLastUpdatedTime, &updated}} {
				syscall.SyscallN(timeline.method(field.slot), uintptr(unsafe.Pointer(timeline)), uintptr(unsafe.Pointer(field.value)))
			}
			timeline.release()
			t.Position, t.Length = trackTimes(start, end, position, updated, t.Playing, time.Now())
		}
		if hasThumbnail {
			t.Art = "/api/art?v=" + artVersion(t.Title+"\x00"+t.Artist+"\x00"+t.Player)
		}
		return nil
	})
	return t
}

// playerName shortens a session's app id: "Spotify.exe" is "Spotify", and a
// Store app's "Family!App" is "App".
func playerName(appID string) string {
	if i := strings.LastIndexAny(appID, `\!`); i >= 0 {
		appID = appID[i+1:]
	}
	if strings.HasSuffix(strings.ToLower(appID), ".exe") {
		appID = appID[:len(appID)-4]
	}
	return appID
}

// trackTimes turns a session timeline, in 100 ns ticks, into seconds. Players
// report the position only now and then, so while playing it moves on from
// the time of the last report.
func trackTimes(start, end, position, updated int64, playing bool, now time.Time) (pos, length float64) {
	length = float64(end-start) / ticksPerSecond
	pos = float64(position-start) / ticksPerSecond
	if playing && updated > 0 {
		if elapsed := float64(now.UnixNano()/100+ticksTo1970-updated) / ticksPerSecond; elapsed > 0 {
			pos += elapsed
		}
	}
	if length > 0 && pos > length {
		pos = length
	}
	return max(pos, 0), max(length, 0)
}

// platformCover reads the session's thumbnail through a DataReader.
func platformCover() ([]byte, error) {
	var data []byte
	err := withSession(func(session *comObject) error {
		props, err := mediaProperties(session)
		if err != nil {
			return err
		}
		defer props.release()
		ref, err := getObject(props, slotThumbnail)
		if err != nil || ref == nil {
			return errNoArt
		}
		defer ref.release()
		var stream *comObject
		if err := callAsync(ref, slotOpenReadAsync, &iidAsyncOperationStream, unsafe.Pointer(&stream)); err != nil {
			return err
		}
		if stream == nil {
			return errNoArt
		}
		defer stream.release()
		data, err = readStream(stream)
		return err
	})
	return data, err
}

// readStream reads a whole IRandomAccessStream of at most maxArtBytes.
func readStream(stream *comObject) ([]byte, error) {
	random, err := queryInterface(stream, &iidIRandomAccessStream)
	if err != nil {
		return nil, err
	}
	defer random.release()
	var size uint64
	hr, _, _ := syscall.SyscallN(random.method(slotStreamGetSize), uintptr(unsafe.Pointer(random)), uintptr(unsafe.Pointer(&size)))
	if err := hresult(hr); err != nil {
		return nil, err
	}
	if err := checkCoverSize(size); err != nil {
		return nil, err
	}
	input, err := queryInterface(stream, &iidIInputStream)
	if err != nil {
		return nil, err
	}
	defer input.release()
	factory, err := activationFactory(classDataReader, &iidIDataReaderFactory)
	if err != nil {
		return nil, err
	}
	defer factory.release()
	var reader *comObject
	hr, _, _ = syscall.SyscallN(factory.method(slotCreateDataReader),
		uintptr(unsafe.Pointer(factory)), uintptr(unsafe.Pointer(input)), uintptr(unsafe.Pointer(&reader)))
	if err := hresult(hr); err != nil {
		return nil, err
	}
	defer reader.release()
	var loaded uint32
	if err := callAsync(reader, slotLoadAsync, &iidAsyncOperationUint32, unsafe.Pointer(&loaded), uintptr(size)); err != nil {
		return nil, err
	}
	if err := checkCoverSize(uint64(loaded)); err != nil {
		return nil, err
	}
	data := make([]byte, loaded)
	hr, _, _ = syscall.SyscallN(reader.method(slotReadBytes),
		uintptr(unsafe.Pointer(reader)), uintptr(loaded), uintptr(unsafe.Pointer(&data[0])))
	return data, hresult(hr)
}

// checkCoverSize keeps covers to the same limit as on Linux.
func checkCoverSize(size uint64) error {
	if size == 0 || size > maxArtBytes {
		return errNoArt
	}
	return nil
}

var sessionSlots = map[string]int{
	"previous": slotTrySkipPreviousAsync,
	"toggle":   slotTryTogglePlayPauseAsync,
	"next":     slotTrySkipNextAsync,
}

var errRefused = errors.New("the player refused the action")

// Tests swap these.
var (
	sessionAction = func(action string) error {
		return withSession(func(session *comObject) error {
			var done byte
			if err := callAsync(session, sessionSlots[action], &iidAsyncOperationBool, unsafe.Pointer(&done)); err != nil {
				return err
			}
			if done == 0 {
				return errRefused
			}
			return nil
		})
	}
	pressMediaKey = func(action string) error {
		vk := mediaKeys[action]
		return sendKeys([]keyInput{virtualKey(vk, false), virtualKey(vk, true)})
	}
)

// mediaAction asks the current session first, which reaches the player shown
// in the media flyout even when another app has focus. Media keys cover
// players without a session. A timed-out request may still arrive, so it
// gets no key press, which could undo a toggle.
func mediaAction(action string) error {
	if err := sessionAction(action); err == nil || errors.Is(err, errAsyncTimeout) {
		return nil
	}
	return pressMediaKey(action)
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
	procPropVariantClear = windows.NewLazySystemDLL("ole32.dll").NewProc("PropVariantClear")

	clsidMMDeviceEnumerator = windows.GUID{Data1: 0xBCDE0395, Data2: 0xE52F, Data3: 0x467C, Data4: [8]byte{0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E}}
	iidIMMDeviceEnumerator  = windows.GUID{Data1: 0xA95664D2, Data2: 0x9614, Data3: 0x4F35, Data4: [8]byte{0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6}}
	iidIAudioEndpointVolume = windows.GUID{Data1: 0x5CDF2C82, Data2: 0x841E, Data3: 0x4546, Data4: [8]byte{0x97, 0x22, 0x0C, 0xF7, 0x40, 0x78, 0x22, 0x9A}}

	// IPolicyConfig is undocumented but is what the Sound control panel uses
	// to pick the default output; there is no public API for it.
	clsidPolicyConfig = windows.GUID{Data1: 0x870AF99C, Data2: 0x171D, Data3: 0x4F9E, Data4: [8]byte{0xAF, 0x0D, 0xE6, 0x3D, 0xF4, 0x0C, 0x2B, 0xC9}}
	iidIPolicyConfig  = windows.GUID{Data1: 0xF8679F50, Data2: 0x850A, Data3: 0x41CF, Data4: [8]byte{0x9C, 0x72, 0x43, 0x0F, 0x29, 0x02, 0x90, 0xC8}}

	pkeyDeviceFriendlyName = propertyKey{
		fmtid: windows.GUID{Data1: 0xA45C254E, Data2: 0xDF1C, Data3: 0x4EFD, Data4: [8]byte{0x80, 0x20, 0x67, 0xD1, 0x46, 0xA8, 0x50, 0xE0}},
		pid:   14,
	}
)

const (
	clsctxAll          = 0x17
	eRender            = 0
	eConsole           = 0
	eCommunications    = 2 // roles run eConsole, eMultimedia, eCommunications
	deviceStateActive  = 1
	stgmRead           = 0
	vtLPWSTR           = 31
	eNotFound          = 0x80070490 // HRESULT_FROM_WIN32(ERROR_NOT_FOUND): no output device
	defaultOutputLabel = "Audio output"

	// Vtable slots, counting IUnknown's three.
	slotRelease                    = 2
	slotEnumAudioEndpoints         = 3  // IMMDeviceEnumerator
	slotGetDefaultAudioEndpoint    = 4  // IMMDeviceEnumerator
	slotGetCount                   = 3  // IMMDeviceCollection
	slotItem                       = 4  // IMMDeviceCollection
	slotActivate                   = 3  // IMMDevice
	slotOpenPropertyStore          = 4  // IMMDevice
	slotGetID                      = 5  // IMMDevice
	slotGetValue                   = 5  // IPropertyStore
	slotSetDefaultEndpoint         = 13 // IPolicyConfig
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
		return fmt.Errorf("COM error 0x%08X", uint32(hr))
	}
	return nil
}

// propertyKey is a PROPERTYKEY.
type propertyKey struct {
	fmtid windows.GUID
	pid   uint32
}

// propVariant is a PROPVARIANT holding a string; the last word pads the
// union to its full size.
type propVariant struct {
	vt  uint16
	_   [3]uint16
	str *uint16
	_   uintptr
}

// withCOM runs fn with COM set up on the current thread.
func withCOM(fn func() error) error {
	// COM is set up per thread, so stay on this one until done.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if hr, _, _ := procCoInitializeEx.Call(0, windows.COINIT_MULTITHREADED); int32(hr) >= 0 {
		defer windows.CoUninitialize()
	}
	return fn()
}

func createInstance(clsid, iid *windows.GUID) (*comObject, error) {
	var object *comObject
	hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(clsid)), 0, clsctxAll, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&object)))
	if err := hresult(hr); err != nil {
		return nil, err
	}
	return object, nil
}

// withEnumerator runs fn with the Core Audio device enumerator.
func withEnumerator(fn func(enumerator *comObject) error) error {
	return withCOM(func() error {
		enumerator, err := createInstance(&clsidMMDeviceEnumerator, &iidIMMDeviceEnumerator)
		if err != nil {
			return err
		}
		defer enumerator.release()
		return fn(enumerator)
	})
}

// defaultOutput returns the default output device, or errNoSpeakers.
func defaultOutput(enumerator *comObject) (*comObject, error) {
	var device *comObject
	hr, _, _ := syscall.SyscallN(enumerator.method(slotGetDefaultAudioEndpoint),
		uintptr(unsafe.Pointer(enumerator)), eRender, eConsole, uintptr(unsafe.Pointer(&device)))
	if uint32(hr) == eNotFound || (hr == 0 && device == nil) {
		return nil, errNoSpeakers
	}
	if err := hresult(hr); err != nil {
		return nil, err
	}
	return device, nil
}

// withSpeakers runs fn with the volume control of the default output.
func withSpeakers(fn func(volume *comObject) error) error {
	return withEnumerator(func(enumerator *comObject) error {
		device, err := defaultOutput(enumerator)
		if err != nil {
			return err
		}
		defer device.release()

		var volume *comObject
		hr, _, _ := syscall.SyscallN(device.method(slotActivate),
			uintptr(unsafe.Pointer(device)), uintptr(unsafe.Pointer(&iidIAudioEndpointVolume)), clsctxAll, 0,
			uintptr(unsafe.Pointer(&volume)))
		if err := hresult(hr); err != nil {
			return err
		}
		defer volume.release()
		return fn(volume)
	})
}

// deviceID returns an endpoint's id, such as "{0.0.0.00000000}.{guid}".
func deviceID(device *comObject) (string, error) {
	var id *uint16
	hr, _, _ := syscall.SyscallN(device.method(slotGetID), uintptr(unsafe.Pointer(device)), uintptr(unsafe.Pointer(&id)))
	if err := hresult(hr); err != nil {
		return "", err
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(id))
	return windows.UTF16PtrToString(id), nil
}

// friendlyName returns the name the Sound settings show, such as
// "Speakers (Realtek(R) Audio)".
func friendlyName(device *comObject) (string, error) {
	var store *comObject
	hr, _, _ := syscall.SyscallN(device.method(slotOpenPropertyStore),
		uintptr(unsafe.Pointer(device)), stgmRead, uintptr(unsafe.Pointer(&store)))
	if err := hresult(hr); err != nil {
		return "", err
	}
	defer store.release()
	var value propVariant
	hr, _, _ = syscall.SyscallN(store.method(slotGetValue),
		uintptr(unsafe.Pointer(store)), uintptr(unsafe.Pointer(&pkeyDeviceFriendlyName)), uintptr(unsafe.Pointer(&value)))
	if err := hresult(hr); err != nil {
		return "", err
	}
	defer func() { procPropVariantClear.Call(uintptr(unsafe.Pointer(&value))) }()
	if value.vt != vtLPWSTR || value.str == nil {
		return "", nil
	}
	return windows.UTF16PtrToString(value.str), nil
}

// listOutputs lists the active output endpoints. The name is the endpoint
// id, which setOutput takes back.
func listOutputs() ([]output, error) {
	outputs := []output{}
	err := withEnumerator(func(enumerator *comObject) error {
		current := ""
		if device, err := defaultOutput(enumerator); err == nil {
			current, err = deviceID(device)
			device.release()
			if err != nil {
				return err
			}
		} else if !errors.Is(err, errNoSpeakers) {
			return err
		}

		var collection *comObject
		hr, _, _ := syscall.SyscallN(enumerator.method(slotEnumAudioEndpoints),
			uintptr(unsafe.Pointer(enumerator)), eRender, deviceStateActive, uintptr(unsafe.Pointer(&collection)))
		if err := hresult(hr); err != nil {
			return err
		}
		defer collection.release()
		var count uint32
		hr, _, _ = syscall.SyscallN(collection.method(slotGetCount),
			uintptr(unsafe.Pointer(collection)), uintptr(unsafe.Pointer(&count)))
		if err := hresult(hr); err != nil {
			return err
		}
		for i := range count {
			var device *comObject
			hr, _, _ = syscall.SyscallN(collection.method(slotItem),
				uintptr(unsafe.Pointer(collection)), uintptr(i), uintptr(unsafe.Pointer(&device)))
			if err := hresult(hr); err != nil {
				return err
			}
			id, err := deviceID(device)
			// A missing name isn't worth hiding the output for.
			label, _ := friendlyName(device)
			device.release()
			if err != nil {
				return err
			}
			if label == "" {
				label = defaultOutputLabel
			}
			outputs = append(outputs, output{Name: id, Label: label, Active: id == current})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return outputs, nil
}

// setOutput makes the endpoint the default for every role, as the Sound
// settings do, so games, music and calls all move to it.
func setOutput(name string) error {
	id, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	return withCOM(func() error {
		policy, err := createInstance(&clsidPolicyConfig, &iidIPolicyConfig)
		if err != nil {
			return err
		}
		defer policy.release()
		for role := eConsole; role <= eCommunications; role++ {
			hr, _, _ := syscall.SyscallN(policy.method(slotSetDefaultEndpoint),
				uintptr(unsafe.Pointer(policy)), uintptr(unsafe.Pointer(id)), uintptr(role))
			if err := hresult(hr); err != nil {
				return err
			}
		}
		return nil
	})
}

// policyConfigFound remembers success only, so a passing COM failure doesn't
// hide outputs until the next restart.
var policyConfigFound atomic.Bool

// outputsAvailable says whether outputs can be switched: the undocumented
// IPolicyConfig may be missing, such as under Wine or on a future Windows.
func outputsAvailable() bool {
	if policyConfigFound.Load() {
		return true
	}
	found := withCOM(func() error {
		policy, err := createInstance(&clsidPolicyConfig, &iidIPolicyConfig)
		if err == nil {
			policy.release()
		}
		return err
	}) == nil
	if found {
		policyConfigFound.Store(true)
	}
	return found
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
