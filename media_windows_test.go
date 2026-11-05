package main

import (
	"errors"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestPlayerName(t *testing.T) {
	for in, want := range map[string]string{
		"Spotify.exe": "Spotify",
		"MSEdge":      "MSEdge",
		"Microsoft.ZuneMusic_8wekyb3d8bbwe!Microsoft.ZuneMusic": "Microsoft.ZuneMusic",
		`C:\Program Files\VideoLAN\VLC\vlc.EXE`:                 "vlc",
		"":                                                      "",
	} {
		if got := playerName(in); got != want {
			t.Errorf("playerName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTrackTimes(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	at := func(ago time.Duration) int64 { return now.Add(-ago).UnixNano()/100 + ticksTo1970 }
	for _, tc := range []struct {
		name                          string
		start, end, position, updated int64
		playing                       bool
		pos, length                   float64
	}{
		{"paused", 0, 200e7, 30e7, at(10 * time.Second), false, 30, 200},
		{"playing moves on", 0, 200e7, 30e7, at(10 * time.Second), true, 40, 200},
		{"never past the end", 0, 200e7, 195e7, at(10 * time.Second), true, 200, 200},
		{"start offset", 10e7, 110e7, 60e7, 0, true, 50, 100},
		{"no timeline", 0, 0, 0, 0, true, 0, 0},
		{"live stream with no length", 0, 0, 30e7, at(5 * time.Second), true, 35, 0},
	} {
		pos, length := trackTimes(tc.start, tc.end, tc.position, tc.updated, tc.playing, now)
		if pos != tc.pos || length != tc.length {
			t.Errorf("%s: got %v/%v, want %v/%v", tc.name, pos, length, tc.pos, tc.length)
		}
	}
}

func TestCheckCoverSize(t *testing.T) {
	for size, ok := range map[uint64]bool{0: false, 1: true, maxArtBytes: true, maxArtBytes + 1: false} {
		if err := checkCoverSize(size); (err == nil) != ok {
			t.Errorf("checkCoverSize(%d) = %v", size, err)
		}
	}
}

func TestMediaActionFallsBackToKeys(t *testing.T) {
	savedSession, savedKey := sessionAction, pressMediaKey
	defer func() { sessionAction, pressMediaKey = savedSession, savedKey }()
	var pressed []string
	pressMediaKey = func(action string) error { pressed = append(pressed, action); return nil }

	sessionAction = func(string) error { return nil }
	if err := mediaAction("toggle"); err != nil || len(pressed) != 0 {
		t.Fatalf("session handled it, yet keys = %v, err %v", pressed, err)
	}
	sessionAction = func(string) error { return errAsyncTimeout }
	if err := mediaAction("toggle"); err != nil || len(pressed) != 0 {
		t.Fatalf("timed out, yet keys = %v, err %v", pressed, err)
	}
	for _, err := range []error{errNoSession, errRefused, errors.New("activation failed")} {
		sessionAction = func(string) error { return err }
		if e := mediaAction("next"); e != nil {
			t.Fatal(e)
		}
	}
	if len(pressed) != 3 || pressed[0] != "next" {
		t.Errorf("keys = %v, want next three times", pressed)
	}
	for action := range mediaActions {
		if _, ok := sessionSlots[action]; !ok {
			t.Errorf("no session method for %q", action)
		}
	}
}

func TestHString(t *testing.T) {
	if procWindowsCreateString.Find() != nil {
		t.Skip("no WinRT strings here")
	}
	h, err := newHString("héllo ğüş 🎵")
	if err != nil {
		t.Fatal(err)
	}
	if got := takeHString(h); got != "héllo ğüş 🎵" {
		t.Errorf("round trip = %q", got)
	}
	if got := takeHString(0); got != "" {
		t.Errorf("null HSTRING = %q", got)
	}
}

// A fake IAsyncOperation<UINT32> and its IAsyncInfo, built from Go callbacks,
// drive await through each way an operation can end.
var fakeAsync struct {
	once      sync.Once
	op, info  comObject
	opVtbl    [32]uintptr
	infoVtbl  [32]uintptr
	opIID     windows.GUID
	statuses  []int32 // returned by get_Status in turn; the last one repeats
	errorCode int32
	result    uint32
	cancelled bool
	refs      int
}

func setupFakeAsync() *comObject {
	f := &fakeAsync
	f.once.Do(func() {
		qi := syscall.NewCallback(func(this uintptr, iid *windows.GUID, out **comObject) uintptr {
			var o *comObject
			switch *iid {
			case f.opIID:
				o = &f.op
			case iidIAsyncInfo:
				o = &f.info
			default:
				return 0x80004002 // E_NOINTERFACE
			}
			f.refs++
			*out = o
			return 0
		})
		release := syscall.NewCallback(func(this uintptr) uintptr { f.refs--; return 0 })
		f.opVtbl[slotQueryInterface], f.infoVtbl[slotQueryInterface] = qi, qi
		f.opVtbl[slotRelease], f.infoVtbl[slotRelease] = release, release
		f.opVtbl[slotAsyncGetResults] = syscall.NewCallback(func(this uintptr, out *uint32) uintptr {
			*out = f.result
			return 0
		})
		f.infoVtbl[slotAsyncGetStatus] = syscall.NewCallback(func(this uintptr, out *int32) uintptr {
			*out = f.statuses[0]
			if len(f.statuses) > 1 {
				f.statuses = f.statuses[1:]
			}
			return 0
		})
		f.infoVtbl[slotAsyncGetError] = syscall.NewCallback(func(this uintptr, out *int32) uintptr {
			*out = f.errorCode
			return 0
		})
		f.infoVtbl[slotAsyncCancel] = syscall.NewCallback(func(this uintptr) uintptr { f.cancelled = true; return 0 })
		f.op.vtbl, f.info.vtbl = &f.opVtbl, &f.infoVtbl
		f.opIID = iidAsyncOperationUint32
	})
	f.cancelled, f.refs, f.result, f.errorCode = false, 1, 0, 0
	return &f.op
}

func TestAwait(t *testing.T) {
	savedTimeout := awaitTimeout
	defer func() { awaitTimeout = savedTimeout }()
	awaitTimeout = 50 * time.Millisecond

	op := setupFakeAsync()
	fakeAsync.statuses, fakeAsync.result = []int32{asyncStarted, asyncStarted, asyncCompleted}, 42
	var got uint32
	if err := await(op, &iidAsyncOperationUint32, unsafe.Pointer(&got)); err != nil || got != 42 {
		t.Fatalf("completed: %d, %v", got, err)
	}
	if fakeAsync.refs != 0 {
		t.Errorf("completed: %d references left", fakeAsync.refs)
	}

	op = setupFakeAsync()
	fakeAsync.statuses, fakeAsync.errorCode = []int32{3}, -2147024891 // Error, E_ACCESSDENIED
	if err := await(op, &iidAsyncOperationUint32, unsafe.Pointer(&got)); err == nil {
		t.Error("error status should fail")
	}

	op = setupFakeAsync()
	fakeAsync.statuses = []int32{asyncStarted}
	if err := await(op, &iidAsyncOperationUint32, unsafe.Pointer(&got)); !errors.Is(err, errAsyncTimeout) || !fakeAsync.cancelled {
		t.Errorf("hung: %v, cancelled %v", err, fakeAsync.cancelled)
	}

	// The wrong result type is refused before GetResults can write anything.
	op = setupFakeAsync()
	fakeAsync.statuses = []int32{asyncCompleted}
	if err := await(op, &iidAsyncOperationBool, unsafe.Pointer(&got)); err == nil {
		t.Error("wrong IAsyncOperation<T> should fail")
	}
	if fakeAsync.refs != 0 {
		t.Errorf("refused: %d references left", fakeAsync.refs)
	}
}

// The real session is checked end to end in the VM; here it must just not
// crash, whether or not this Windows has one.
func TestNowPlayingWindows(t *testing.T) {
	if tr := nowPlaying(); tr != nil && tr.Title == "" {
		t.Errorf("track without a title: %+v", tr)
	}
	if _, err := platformCover(); err == nil && !nowPlayingAvailable() {
		t.Error("cover without the media session API")
	}
}
