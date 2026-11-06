package server

import (
	"strconv"
	"strings"
	"time"
)

// How often the lens hears where the pointer is: about once a frame.
const lensFeedEvery = 16 * time.Millisecond

// lensDriver is the desktop's own zoom lens: Quickshell on Hyprland, the
// Magnifier control on Windows.
type lensDriver interface {
	// available reports whether this desktop can zoom at all.
	available() bool
	// start puts the lens up; its run ends when stopped or on its own.
	start(s pointerSettings, look lensLook) (*lensRun, error)
	// hideCursor hides the real cursor while a lens draws its own.
	hideCursor(hidden bool)
	// restore undoes anything a crash could have left behind.
	restore()
}

// screenRect is a screen, or part of one, in desktop pixels.
type screenRect struct {
	Left, Top, Width, Height int32
}

// lensRing is how wide the ring around the lens is, as on Linux.
const lensRing = 3

// lensPlacement is where the round lens window goes and the part of the
// screen it shows.
type lensPlacement struct {
	WinX, WinY int32
	Src        screenRect
}

// placeLens centers the lens on the pointer and shows the screen around it.
// At an edge the shown part stops at the screen, and the lens slides
// instead, so the magnified spot under the real pointer is the one a click
// lands on.
func placeLens(x, y, size int32, zoom int, screen screenRect) lensPlacement {
	z := int32(max(zoom, 1))
	side := (size - 2*lensRing) / z
	srcX := clampSpan(x-side/2, screen.Left, screen.Left+screen.Width-side)
	srcY := clampSpan(y-side/2, screen.Top, screen.Top+screen.Height-side)
	return lensPlacement{
		WinX: x - lensRing - (x-srcX)*z,
		WinY: y - lensRing - (y-srcY)*z,
		Src:  screenRect{Left: srcX, Top: srcY, Width: side, Height: side},
	}
}

func clampSpan(v, low, high int32) int32 {
	return max(low, min(v, high))
}

// colorRef turns a theme color like #C8F24A into a Windows COLORREF
// (0x00BBGGRR), with the default ring for anything else.
func colorRef(color string) uint32 {
	hex := strings.TrimPrefix(color, "#")
	switch len(hex) {
	case 3, 4:
		hex = string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
	case 6, 8:
		hex = hex[:6]
	default:
		return colorRef(defaultLensLook().Ring)
	}
	v, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return colorRef(defaultLensLook().Ring)
	}
	return uint32(v>>16&0xFF | v&0xFF00 | (v&0xFF)<<16)
}
