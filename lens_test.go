package main

import "testing"

// The lens follows the pointer. At an edge the part of the screen it shows
// stops, and the lens slides instead, so the magnified spot under the real
// pointer is still the spot a click lands on.
func TestPlaceLensKeepsThePointerTrue(t *testing.T) {
	screens := []screenRect{
		{Left: 0, Top: 0, Width: 1920, Height: 1080},
		{Left: -1280, Top: -200, Width: 1280, Height: 800}, // left of and above the primary
	}
	for _, screen := range screens {
		for _, size := range lensSizes {
			for zoom := 2; zoom <= 4; zoom++ {
				for _, x := range []int32{screen.Left, screen.Left + 1, screen.Left + 37, screen.Left + screen.Width/2, screen.Left + screen.Width - 2, screen.Left + screen.Width - 1} {
					for _, y := range []int32{screen.Top, screen.Top + 5, screen.Top + screen.Height/2, screen.Top + screen.Height - 1} {
						p := placeLens(x, y, int32(size), zoom, screen)
						src := p.Src
						if src.Left < screen.Left || src.Top < screen.Top ||
							src.Left+src.Width > screen.Left+screen.Width || src.Top+src.Height > screen.Top+screen.Height {
							t.Fatalf("%v size %d %dx at %d,%d: source %+v leaves the screen", screen, size, zoom, x, y, src)
						}
						if want := (int32(size) - 2*lensRing) / int32(zoom); src.Width != want || src.Height != want {
							t.Fatalf("source %+v, want %d square", src, want)
						}
						// Where the pointer's spot shows up in the lens.
						shownX := p.WinX + lensRing + (x-src.Left)*int32(zoom)
						shownY := p.WinY + lensRing + (y-src.Top)*int32(zoom)
						if abs32(shownX-x) > int32(zoom) || abs32(shownY-y) > int32(zoom) {
							t.Fatalf("%v size %d %dx at %d,%d: the spot shows at %d,%d", screen, size, zoom, x, y, shownX, shownY)
						}
					}
				}
			}
		}
	}
}

func TestPlaceLensCentersAwayFromEdges(t *testing.T) {
	screen := screenRect{Width: 1920, Height: 1080}
	tests := []struct {
		name       string
		x, y       int32
		size       int32
		zoom       int
		wx, wy     int32
		srcX, srcY int32
	}{
		// 280 across, 274 inside the ring, 137 (2x) of screen shown.
		{"middle 2x", 960, 540, 280, 2, 821, 401, 892, 472},
		{"middle 4x", 960, 540, 280, 4, 821, 401, 926, 506},
		{"top left corner", 0, 0, 280, 2, -3, -3, 0, 0},
		{"bottom right corner", 1919, 1079, 280, 2, 1916 - 2*136, 1076 - 2*136, 1920 - 137, 1080 - 137},
		{"left edge", 10, 540, 280, 2, -13, 401, 0, 472},
		{"right edge 3x", 1915, 540, 200, 3, 1912 - 3*(1915-1856), 441, 1856, 508},
	}
	for _, tt := range tests {
		p := placeLens(tt.x, tt.y, tt.size, tt.zoom, screen)
		if p.WinX != tt.wx || p.WinY != tt.wy || p.Src.Left != tt.srcX || p.Src.Top != tt.srcY {
			t.Errorf("%s: window %d,%d source %d,%d; want window %d,%d source %d,%d",
				tt.name, p.WinX, p.WinY, p.Src.Left, p.Src.Top, tt.wx, tt.wy, tt.srcX, tt.srcY)
		}
	}
}

func TestColorRef(t *testing.T) {
	tests := map[string]uint32{
		"#C8F24A":   0x4AF2C8,
		"#c8f24a80": 0x4AF2C8, // alpha dropped
		"#fff":      0xFFFFFF,
		"#1234":     0x332211,
		"#12345":    0x4AF2C8, // not a color: the default ring
	}
	for in, want := range tests {
		if got := colorRef(in); got != want {
			t.Errorf("colorRef(%q) = %#06x, want %#06x", in, got, want)
		}
	}
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}
