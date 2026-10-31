package main

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"testing"
)

func readPNG(t *testing.T, name string) image.Image {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	m, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// The light tray icon is the dark one with every color set to white and the
// shape (alpha) unchanged. Regenerate it that way if trayicon.png changes.
func TestLightTrayIconMatchesTheDarkOne(t *testing.T) {
	dark, light := readPNG(t, "trayicon.png"), readPNG(t, "trayicon_light.png")
	if dark.Bounds() != light.Bounds() {
		t.Fatalf("sizes differ: %v and %v", dark.Bounds(), light.Bounds())
	}
	visible := 0
	b := dark.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, da := dark.At(x, y).RGBA()
			lr, lg, lb, la := light.At(x, y).RGBA()
			if da != la {
				t.Fatalf("alpha differs at %d,%d: %d and %d", x, y, da, la)
			}
			if la == 0 {
				continue
			}
			visible++
			// RGBA() is premultiplied: white at alpha a is (a, a, a).
			if lr != la || lg != la || lb != la {
				t.Fatalf("pixel %d,%d is not white: %d %d %d a=%d", x, y, lr, lg, lb, la)
			}
		}
	}
	if visible == 0 {
		t.Fatal("the icon is empty")
	}
}
