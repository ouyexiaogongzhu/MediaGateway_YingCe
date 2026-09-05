package service

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
	"testing"
)

// buildSheetPNG 合成一张四格拼图：白色 8px 留白分隔，每格为竖向渐变的纯色底，
// 避免整格匀色被误判为留白列。
func buildSheetPNG(t *testing.T, panelWidth, panelHeight int) string {
	t.Helper()
	gap := 8
	panels := 4
	width := panels*panelWidth + (panels-1)*gap
	height := panelHeight
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	bases := []color.RGBA{
		{R: 120, G: 60, B: 60, A: 255},
		{R: 60, G: 120, B: 60, A: 255},
		{R: 60, G: 60, B: 160, A: 255},
		{R: 140, G: 120, B: 40, A: 255},
	}
	for p := 0; p < panels; p++ {
		left := p * (panelWidth + gap)
		for y := 0; y < height; y++ {
			shade := uint8(int(bases[p].R)*0 + y*120/height) // 竖向渐变，保证列内有灰度差
			for x := left; x < left+panelWidth; x++ {
				img.SetRGBA(x, y, color.RGBA{
					R: bases[p].R + shade/2,
					G: bases[p].G + shade/3,
					B: bases[p].B + shade/4,
					A: 255,
				})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode test png: %v", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func decodeDataURLImage(t *testing.T, dataURL string) image.Image {
	t.Helper()
	comma := strings.Index(dataURL, ",")
	if comma < 0 {
		t.Fatalf("not a dataURL: %.40s", dataURL)
	}
	raw, err := base64.StdEncoding.DecodeString(dataURL[comma+1:])
	if err != nil {
		t.Fatalf("bad base64: %v", err)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode panel: %v", err)
	}
	return img
}

func TestSplitCharacterSheetFourPanels(t *testing.T) {
	sheet := buildSheetPNG(t, 300, 400) // 总宽 1224×400，单格 300 宽
	panels, ok := splitCharacterSheetDataUrl(sheet)
	if !ok {
		t.Fatal("expected four-panel sheet to be split")
	}
	if len(panels) != 2 {
		t.Fatalf("expected 2 panels (closeup+front), got %d", len(panels))
	}
	for i, panel := range panels {
		img := decodeDataURLImage(t, panel)
		if got := img.Bounds().Dx(); got != 300 {
			t.Fatalf("panel %d width = %d, want panel-level 300", i, got)
		}
		if got := img.Bounds().Dy(); got != 400 {
			t.Fatalf("panel %d height = %d, want 400", i, got)
		}
	}
	// 两格颜色基调不同，确认确实裁到了不同的格。
	first := decodeDataURLImage(t, panels[0]).At(10, 10)
	second := decodeDataURLImage(t, panels[1]).At(10, 10)
	if first == second {
		t.Fatal("panel 1 and panel 2 have identical base color; split likely wrong")
	}
}

func TestSplitCharacterSheetLandscapePhoto(t *testing.T) {
	// 普通横幅风景图：全宽渐变、无留白列。
	img := image.NewRGBA(image.Rect(0, 0, 960, 540))
	for y := 0; y < 540; y++ {
		for x := 0; x < 960; x++ {
			v := uint8((x*255)/960 + (y*60)/540)
			img.SetRGBA(x, y, color.RGBA{R: v, G: uint8(y * 255 / 540), B: 90, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if _, ok := splitCharacterSheetDataUrl("data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())); ok {
		t.Fatal("plain landscape photo should not be split")
	}
}

func TestSplitCharacterSheetPortraitRejected(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 400, 900))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if _, ok := splitCharacterSheetDataUrl("data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())); ok {
		t.Fatal("portrait image (w<h) must not be split")
	}
}

func TestSplitCharacterSheetBadInput(t *testing.T) {
	if _, ok := splitCharacterSheetDataUrl("data:image/png;base64,@@not-base64@@"); ok {
		t.Fatal("bad base64 must return false")
	}
	if _, ok := splitCharacterSheetDataUrl("https://example.com/a.png"); ok {
		t.Fatal("non-dataURL must return false")
	}
}
