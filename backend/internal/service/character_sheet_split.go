package service

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/draw"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strings"
	"time"

	"infinite-canvas/backend/internal/protocol"
)

// 角色三视图定妆常被做成「特写|正面|侧面|背面」四格横幅拼图；整张作为单个
// 编辑参考发给生图模型会把四格人物平均，还原变差。这里做自动裁切：
// 证据充分（≥3 个有效面板）时裁出全部格（特写/正面/侧面/背面…），
// 任何失败都退回原图。
const (
	sheetMinWidth          = 600 // 拼图必为宽幅横幅
	sheetMinPanelWidth     = 100 // 单格最小宽度
	sheetMinSeparatorWidth = 1   // 真实拼图的分隔线可能仅 1-3px 细亮线；误报由「均匀+近白/近背景」双条件兜底
	sheetBlankDiffPerPair  = 24  // 列内相邻采样灰度差 ≤24 视为匀色（分隔线自带竖向渐晕，实测 maxDiff 15-22）
	sheetMinValidPanels    = 3   // 至少 3 格才认定是拼图
)

// splitCharacterSheetDataUrl 检测 dataURL 图片是否为多格角色三视图拼图；
// 是则裁出前两格（特写+正面）返回两个 png dataURL；否则返回 (nil, false)。
func splitCharacterSheetDataUrl(dataURL string) ([]string, bool) {
	comma := strings.Index(dataURL, ",")
	if comma < 0 || !strings.HasPrefix(strings.ToLower(dataURL[:comma]), "data:image/") {
		fmt.Println("[sheet-split] skip: not a data:image dataURL")
		return nil, false
	}
	raw := decodeDataURLBytes(dataURL)
	if raw == nil {
		fmt.Println("[sheet-split] skip: base64 decode failed")
		return nil, false
	}
	panels, ok := splitCharacterSheetBytes(raw)
	if !ok {
		return nil, false
	}
	result := make([]string, 0, len(panels))
	for _, p := range panels {
		result = append(result, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(p))
	}
	return result, true
}

// splitCharacterSheetBytes 对原始图片字节做三视图拼图裁切，返回前两格的 png 字节。
// 永不返回错误：任何解码/裁切失败都按「不是拼图」处理，不阻塞生成。
func splitCharacterSheetBytes(raw []byte) ([][]byte, bool) {
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		fmt.Println("[sheet-split] skip: image decode failed")
		return nil, false
	}
	panels, ok := splitCharacterSheetImage(src)
	if !ok {
		fmt.Printf("[sheet-split] skip: not a multi-panel collage (%dx%d)\n",
			src.Bounds().Dx(), src.Bounds().Dy())
		return nil, false
	}
	out := make([][]byte, 0, len(panels))
	for _, panel := range panels {
		var buf bytes.Buffer
		if err := png.Encode(&buf, panel); err != nil {
			return nil, false
		}
		out = append(out, buf.Bytes())
	}
	return out, true
}

// splitCharacterSheetImage 通过「列留白扫描」找分隔线，把横幅拼图切成竖版面板。
func splitCharacterSheetImage(src image.Image) ([]image.Image, bool) {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width < sheetMinWidth || width <= height {
		return nil, false
	}
	blank := sheetBlankColumns(src)
	type segment struct{ start, end int }
	var separators []segment
	for x := 0; x < width; {
		if blank[x] {
			start := x
			for x < width && blank[x] {
				x++
			}
			if x-start >= sheetMinSeparatorWidth {
				separators = append(separators, segment{start, x})
			}
		} else {
			x++
		}
	}
	// 分隔线之间（含图像两端）的面板候选。
	var panels []segment
	cursor := 0
	flushPanel := func(end int) {
		if end-cursor >= sheetMinPanelWidth && height*10 >= (end-cursor)*9 {
			panels = append(panels, segment{cursor, end})
		}
	}
	for _, sep := range separators {
		flushPanel(sep.start)
		cursor = sep.end
	}
	flushPanel(width)
	if len(panels) < sheetMinValidPanels {
		return nil, false
	}
	crops := make([]image.Image, 0, len(panels))
	for _, panel := range panels {
		rect := image.Rect(bounds.Min.X+panel.start, bounds.Min.Y, bounds.Min.X+panel.end, bounds.Min.Y+height)
		crop := image.NewRGBA(image.Rect(0, 0, panel.end-panel.start, height))
		draw.Draw(crop, crop.Bounds(), src, rect.Min, draw.Src)
		crops = append(crops, crop)
	}
	return crops, true
}

// sheetBlankColumns 判定「分隔列」：整列均匀（列内相邻采样灰度差 ≤24），且
// 左右两侧 32-96px 窗口的内容均值色都与该列均值的最大通道差 ≥25 ——
// 面板内部的匀色列两侧是同类内容，天然不会成为分隔候选；
// 不做任何背景色估计（摄影灰底的渐晕会让固定/插值背景全部失配）。
func sheetBlankColumns(src image.Image) []bool {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	step := height / 120
	if step < 1 {
		step = 1
	}
	mean := make([][3]int, width)
	uniform := make([]bool, width)
	for x := 0; x < width; x++ {
		var sum [3]int
		maxDiff, prev, n := 0, -1, 0
		for y := 0; y < height; y += step {
			r, g, b, _ := src.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			r8, g8, b8 := int(r>>8), int(g>>8), int(b>>8)
			sum[0] += r8
			sum[1] += g8
			sum[2] += b8
			lum := (r8*299 + g8*587 + b8*114) / 1000
			if prev >= 0 {
				d := lum - prev
				if d < 0 {
					d = -d
				}
				if d > maxDiff {
					maxDiff = d
				}
			}
			prev = lum
			n++
		}
		mean[x] = [3]int{sum[0] / n, sum[1] / n, sum[2] / n}
		uniform[x] = n > 0 && maxDiff <= sheetBlankDiffPerPair
	}
	chDelta := func(a, b [3]int) int {
		dr, dg, db := a[0]-b[0], a[1]-b[1], a[2]-b[2]
		m := dr
		if abs(dg) > m {
			m = abs(dg)
		}
		if abs(db) > m {
			m = abs(db)
		}
		if m < 0 {
			m = -m
		}
		return m
	}
	const winNear, winFar = 32, 96
	blank := make([]bool, width)
	for x := 0; x < width; x++ {
		if !uniform[x] {
			continue
		}
		lo, hi := x-winFar, x+winFar
		if lo < bounds.Min.X {
			lo = bounds.Min.X
		}
		if hi > bounds.Max.X-1 {
			hi = bounds.Max.X - 1
		}
		leftMean := mean[lo]
		rightMean := mean[hi]
		// 两侧窗口远离候选列，取窗口最远端均值做对比
		if chDelta(mean[x], leftMean) >= 25 && chDelta(mean[x], rightMean) >= 25 {
			blank[x] = true
		}
	}
	return blank
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// splitCharacterSheetReference 把一条 edit_source 参考替换为裁切后的前两格；
// dataURL 与 URL 引用都支持（URL 下载失败按非拼图处理）；其他类型走原参考。
func splitCharacterSheetReference(item protocol.MediaReference) ([]protocol.MediaReference, bool) {
	var raw []byte
	switch {
	case strings.HasPrefix(strings.ToLower(item.DataURL), "data:image/"):
		raw = decodeDataURLBytes(item.DataURL)
	case item.URL != "":
		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Get(item.URL)
		if err != nil {
			fmt.Println("[sheet-split] skip: fetch URL failed:", err.Error())
			return nil, false
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			fmt.Println("[sheet-split] skip: fetch URL status", resp.StatusCode)
			return nil, false
		}
		buf, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		if err != nil {
			return nil, false
		}
		raw = buf
	default:
		return nil, false
	}
	pngs, ok := splitCharacterSheetBytes(raw)
	if !ok {
		return nil, false
	}
	fmt.Printf("[sheet-split] split ok: %d panels\n", len(pngs))
	suffixes := []string{"_closeup", "_front", "_side", "_back",
		"_view5", "_view6", "_view7", "_view8"}
	split := make([]protocol.MediaReference, 0, len(pngs))
	for index, pngBytes := range pngs {
		panel := item
		panel.DataURL = "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
		panel.URL = ""
		panel.Name = strings.TrimSpace(item.Name) + suffixes[index]
		if item.ID != "" {
			panel.ID = item.ID + suffixes[index]
		}
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(pngBytes)); err == nil {
			panel.Metadata["width"] = cfg.Width
			panel.Metadata["height"] = cfg.Height
		}
		split = append(split, panel)
	}
	return split, true
}

func decodeDataURLBytes(dataURL string) []byte {
	if comma := strings.Index(dataURL, ","); comma >= 0 {
		if raw, err := base64.StdEncoding.DecodeString(dataURL[comma+1:]); err == nil {
			return raw
		}
	}
	return nil
}
