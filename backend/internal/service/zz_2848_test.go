package service

import (
	"bytes"
	"fmt"
	"image"
	"os"
	"testing"
)

func TestZZ2848(t *testing.T) {
	raw, err := os.ReadFile("/tmp/sheet2848.png")
	if err != nil {
		t.Fatal(err)
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	fmt.Printf("size: %dx%d\n", w, h)
	step := h / 120
	if step < 1 {
		step = 1
	}
	blank := sheetBlankColumns(src)
	run, runsList := 0, []int{}
	for x := 0; x < w; x++ {
		if blank[x] {
			run++
		} else {
			if run > 0 {
				runsList = append(runsList, run)
			}
			run = 0
		}
	}
	if run > 0 {
		runsList = append(runsList, run)
	}
	fmt.Printf("blank runs: %v\n", runsList)
	panels, ok := splitCharacterSheetImage(src)
	fmt.Printf("split ok=%v panels=%d\n", ok, len(panels))
	// 重算边缘背景
	var bgL2, bgR2 [3]int
	cnt2 := 0
	srow2 := h / 32
	if srow2 < 1 {
		srow2 = 1
	}
	for y := 0; y < h; y += srow2 {
		for dx := 0; dx < 8; dx++ {
			r, g, b, _ := src.At(bounds.Min.X+dx, bounds.Min.Y+y).RGBA()
			bgL2[0] += int(r >> 8); bgL2[1] += int(g >> 8); bgL2[2] += int(b >> 8)
			r, g, b, _ = src.At(bounds.Max.X-9+dx, bounds.Min.Y+y).RGBA()
			bgR2[0] += int(r >> 8); bgR2[1] += int(g >> 8); bgR2[2] += int(b >> 8)
			cnt2++
		}
	}
	for i := 0; i < 3; i++ { bgL2[i] /= cnt2; bgR2[i] /= cnt2 }
	fmt.Printf("bgL=%v bgR=%v\n", bgL2, bgR2)
	for _, base := range []int{690, 1400, 2120} {
		for x := base; x < base+40; x++ {
			maxDiff, minLum, maxLum, nearBg, samples := 0, 1<<30, 0, true, 0
			var mr, mg, mb int
			prev := -1
			for y := 0; y < h; y += step {
				r, g, b, _ := src.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
				r8, g8, b8 := int(r>>8), int(g>>8), int(b>>8)
				lum := (r8*299 + g8*587 + b8*114) / 1000
				if prev >= 0 {
					d := lum - prev
					if d < 0 { d = -d }
					if d > maxDiff { maxDiff = d }
				}
				if lum < minLum { minLum = lum }
				if lum > maxLum { maxLum = lum }
				t2 := float64(x) / float64(w-1)
				er := bgL2[0] + int(float64(bgR2[0]-bgL2[0])*t2)
				eg := bgL2[1] + int(float64(bgR2[1]-bgL2[1])*t2)
				eb := bgL2[2] + int(float64(bgR2[2]-bgL2[2])*t2)
				dr, dg, db := r8-er, g8-eg, b8-eb
				if dr < 0 { dr = -dr }
				if dg < 0 { dg = -dg }
				if db < 0 { db = -db }
				if dr > 28 || dg > 28 || db > 28 { nearBg = false }
				mr += r8; mg += g8; mb += b8
				samples++
				prev = lum
			}
			if samples == 0 { continue }
			uniform := maxDiff <= 24
			if uniform || !nearBg {
				fmt.Printf("x=%d uni=%v(maxDiff %d) nearBg=%v mean=(%d,%d,%d)\n",
					x, uniform, maxDiff, nearBg, mr/samples, mg/samples, mb/samples)
			}
		}
	}
}
