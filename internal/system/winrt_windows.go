package system

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	combase                    = windows.NewLazySystemDLL("combase.dll")
	procRoInitialize           = combase.NewProc("RoInitialize")
	procRoUninitialize         = combase.NewProc("RoUninitialize")
	procRoGetActivationFactory = combase.NewProc("RoGetActivationFactory")
	procWindowsCreateString    = combase.NewProc("WindowsCreateString")
	procWindowsDeleteString    = combase.NewProc("WindowsDeleteString")
	procWindowsGetStringRaw    = combase.NewProc("WindowsGetStringRawBuffer")
)

// IIDs from the Windows SDK 10.0.26100 headers (winrt/windows.media.control.h,
// winrt/windows.storage.streams.h, winrt/asyncinfo.h). The IAsyncOperation
// ones are the parameterized IIDs the headers list for each result type.
var (
	iidIAsyncInfo                    = mustGUID("00000036-0000-0000-C000-000000000046")
	iidSessionManagerStatics         = mustGUID("2050c4ee-11a0-57de-aed7-c97c70338245")
	iidAsyncOperationSessionManager  = mustGUID("3eec115e-7346-5c27-8c5f-da78514a277b")
	iidAsyncOperationMediaProperties = mustGUID("b185e6f3-e0d8-51cb-913f-c98d48c93c46")
	iidAsyncOperationBool            = mustGUID("cdb5efb3-5788-509d-9be1-71ccb8a3362a")
	iidAsyncOperationStream          = mustGUID("c4a57c5e-32b0-55b3-ad13-ce1c23041ed6") // IRandomAccessStreamWithContentType
	iidAsyncOperationUint32          = mustGUID("ef60385f-be78-584b-aaef-7829ada2b0de")
	iidIRandomAccessStream           = mustGUID("905a0fe1-bc53-11df-8c49-001e4fc686da")
	iidIInputStream                  = mustGUID("905a0fe2-bc53-11df-8c49-001e4fc686da")
	iidIDataReaderFactory            = mustGUID("d7527847-57da-4e15-914c-06806699a098")
)

const (
	roInitMultithreaded = 1

	classSessionManager = "Windows.Media.Control.GlobalSystemMediaTransportControlsSessionManager"
	classDataReader     = "Windows.Storage.Streams.DataReader"

	// AsyncStatus
	asyncStarted   = 0
	asyncCompleted = 1

	// Vtable slots. IInspectable's methods take slots 3-5, so a WinRT
	// interface's own methods start at 6.
	slotQueryInterface   = 0
	slotAsyncGetStatus   = 7  // IAsyncInfo
	slotAsyncGetError    = 8  // IAsyncInfo
	slotAsyncCancel      = 9  // IAsyncInfo
	slotAsyncGetResults  = 8  // IAsyncOperation<T>, after put_Completed and get_Completed
	slotOpenReadAsync    = 6  // IRandomAccessStreamReference
	slotStreamGetSize    = 6  // IRandomAccessStream
	slotCreateDataReader = 6  // IDataReaderFactory
	slotReadBytes        = 14 // IDataReader
	slotLoadAsync        = 29 // IDataReader
)

// awaitTimeout bounds every async call, so a hung player can't hold up the
// status request.
var awaitTimeout = 2 * time.Second

var errAsyncTimeout = errors.New("WinRT call timed out")

func mustGUID(s string) windows.GUID {
	g, err := windows.GUIDFromString("{" + s + "}")
	if err != nil {
		panic(err)
	}
	return g
}

// withWinRT runs fn with the Windows Runtime set up on the current thread.
func withWinRT(fn func() error) error {
	if err := procRoInitialize.Find(); err != nil {
		return err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if hr, _, _ := procRoInitialize.Call(roInitMultithreaded); int32(hr) >= 0 {
		defer procRoUninitialize.Call()
	}
	return fn()
}

// newHString makes an HSTRING; the caller deletes it.
func newHString(s string) (uintptr, error) {
	text, err := windows.UTF16FromString(s)
	if err != nil {
		return 0, err
	}
	var h uintptr
	hr, _, _ := procWindowsCreateString.Call(uintptr(unsafe.Pointer(&text[0])), uintptr(len(text)-1), uintptr(unsafe.Pointer(&h)))
	return h, hresult(hr)
}

// takeHString returns the text of h and deletes it. A null HSTRING is "".
func takeHString(h uintptr) string {
	if h == 0 {
		return ""
	}
	defer procWindowsDeleteString.Call(h)
	var n uint32
	p, _, _ := procWindowsGetStringRaw.Call(h, uintptr(unsafe.Pointer(&n)))
	if p == 0 || n == 0 {
		return ""
	}
	// Read the returned address as a pointer; it stays valid until h is deleted.
	return windows.UTF16ToString(unsafe.Slice(*(**uint16)(unsafe.Pointer(&p)), n))
}

// getString calls a getter that returns an HSTRING.
func getString(o *comObject, slot int) string {
	var h uintptr
	if hr, _, _ := syscall.SyscallN(o.method(slot), uintptr(unsafe.Pointer(o)), uintptr(unsafe.Pointer(&h))); int32(hr) < 0 {
		return ""
	}
	return takeHString(h)
}

// getObject calls a getter that returns an interface pointer, which may be nil.
func getObject(o *comObject, slot int) (*comObject, error) {
	var result *comObject
	hr, _, _ := syscall.SyscallN(o.method(slot), uintptr(unsafe.Pointer(o)), uintptr(unsafe.Pointer(&result)))
	return result, hresult(hr)
}

func activationFactory(class string, iid *windows.GUID) (*comObject, error) {
	if err := procRoGetActivationFactory.Find(); err != nil {
		return nil, err
	}
	name, err := newHString(class)
	if err != nil {
		return nil, err
	}
	defer procWindowsDeleteString.Call(name)
	var factory *comObject
	hr, _, _ := procRoGetActivationFactory.Call(name, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&factory)))
	if err := hresult(hr); err != nil {
		return nil, err
	}
	return factory, nil
}

func queryInterface(o *comObject, iid *windows.GUID) (*comObject, error) {
	var result *comObject
	hr, _, _ := syscall.SyscallN(o.method(slotQueryInterface),
		uintptr(unsafe.Pointer(o)), uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&result)))
	if err := hresult(hr); err != nil {
		return nil, err
	}
	return result, nil
}

// await waits for an IAsyncOperation<T> and writes its result to out, which
// must point at the T that iid names. It releases op.
func await(op *comObject, iid *windows.GUID, out unsafe.Pointer) error {
	defer op.release()
	// Asking for the exact IAsyncOperation<T> proves the result type before
	// GetResults writes through out.
	typed, err := queryInterface(op, iid)
	if err != nil {
		return err
	}
	defer typed.release()
	info, err := queryInterface(op, &iidIAsyncInfo)
	if err != nil {
		return err
	}
	defer info.release()
	deadline := time.Now().Add(awaitTimeout)
	for {
		var status int32
		hr, _, _ := syscall.SyscallN(info.method(slotAsyncGetStatus), uintptr(unsafe.Pointer(info)), uintptr(unsafe.Pointer(&status)))
		if err := hresult(hr); err != nil {
			return err
		}
		switch status {
		case asyncStarted:
			if time.Now().After(deadline) {
				syscall.SyscallN(info.method(slotAsyncCancel), uintptr(unsafe.Pointer(info)))
				return errAsyncTimeout
			}
			time.Sleep(5 * time.Millisecond)
		case asyncCompleted:
			hr, _, _ := syscall.SyscallN(typed.method(slotAsyncGetResults), uintptr(unsafe.Pointer(typed)), uintptr(out))
			return hresult(hr)
		default:
			var code int32
			syscall.SyscallN(info.method(slotAsyncGetError), uintptr(unsafe.Pointer(info)), uintptr(unsafe.Pointer(&code)))
			return fmt.Errorf("WinRT call failed (status %d, 0x%08X)", status, uint32(code))
		}
	}
}

// callAsync calls a method that starts an IAsyncOperation<T> and waits for
// it. args go between the object and the out pointer; at most one is used.
func callAsync(o *comObject, slot int, iid *windows.GUID, out unsafe.Pointer, args ...uintptr) error {
	var op *comObject
	var hr uintptr
	// &op is converted inside each call, so the stack can't move under it.
	switch len(args) {
	case 0:
		hr, _, _ = syscall.SyscallN(o.method(slot), uintptr(unsafe.Pointer(o)), uintptr(unsafe.Pointer(&op)))
	case 1:
		hr, _, _ = syscall.SyscallN(o.method(slot), uintptr(unsafe.Pointer(o)), args[0], uintptr(unsafe.Pointer(&op)))
	default:
		return errors.New("callAsync takes at most one argument")
	}
	if err := hresult(hr); err != nil {
		return err
	}
	if op == nil {
		return errors.New("WinRT call returned no operation")
	}
	return await(op, iid, out)
}
