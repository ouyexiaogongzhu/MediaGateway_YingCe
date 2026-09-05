package service

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/draw"
	_ "image/jpeg"
	"image/png"
	"strings"

	"infinite-canvas/backend/internal/protocol"
)

// 角色三视图定妆常被做成「特写|正面|侧面|背面」四格横幅拼图；整张作为单个
// 编辑参考发给生图模型会把四格人物平均，还原变差。这里做一次保守的自动裁切：
// 只在证据充分（≥3 个有效面板）时裁出前两格（特写+正面），任何失败都退回原图。
const (
	sheetMinWidth          = 600 // 拼图必为宽幅横幅
	sheetMinPanelWidth     = 100 // 单格最小宽度
	sheetMinSeparatorWidth = 4   // 格间留白至少这么宽才算分隔线（滤掉面板内部的偶发匀色列）
	sheetBlankDiffPerPair  = 6   // 相邻采样像素灰度差平均值 ≤6 视为留白列
	sheetMinValidPanels    = 3   // 至少 3 格才认定是拼图
)

// splitCharacterSheetDataUrl 检测 dataURL 图片是否为多格角色三视图拼图；
// 是则裁出前两格（特写+正面）返回两个 png dataURL；否则返回 (nil, false)。
// 永不返回错误：任何解码/裁切失败都按「不是拼图」处理，不阻塞生成。
func splitCharacterSheetDataUrl(dataURL string) ([]string, bool) {
	comma := strings.Index(dataURL, ",")
	if comma < 0 || !strings.HasPrefix(strings.ToLower(dataURL[:comma]), "data:image/") {
		return nil, false
	}
	raw, err := base64.StdEncoding.DecodeString(dataURL[comma+1:])
	if err != nil {
		return nil, false
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, false
	}
	panels, ok := splitCharacterSheetImage(src)
	if !ok {
		return nil, false
	}
	result := make([]string, 0, len(panels))
	for _, panel := range panels {
		var buf bytes.Buffer
		if err := png.Encode(&buf, panel); err != nil {
			return nil, false
		}
		result = append(result, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(buf.Bytes()))
	}
	return result, true
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
	crops := make([]image.Image, 0, 2)
	for _, panel := range panels[:2] {
		rect := image.Rect(bounds.Min.X+panel.start, bounds.Min.Y, bounds.Min.X+panel.end, bounds.Min.Y+height)
		crop := image.NewRGBA(image.Rect(0, 0, panel.end-panel.start, height))
		draw.Draw(crop, crop.Bounds(), src, rect.Min, draw.Src)
		crops = append(crops, crop)
	}
	return crops, true
}

// sheetBlankColumns 判定「留白列」：整列均匀（相邻采样灰度差低）且满足其一——
//   - 近白（亮度 ≥200 且色散小）：定妆拼图惯例的白/浅色分隔带；或
//   - 接近四角一致背景色（深色/彩色底拼图，四角必然是背景）。
//
// 只用「列内梯度」不行：真实三视图整张都是平滑渐变，人物色块列与留白列
// 梯度一样低，会把全部列误判为留白导致永不裁切。
func sheetBlankColumns(src image.Image) []bool {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	step := height / 120
	if step < 1 {
		step = 1
	}
	// 四角各 8x8 的中位色：一致时才作为背景参考（格子顶边时四角各在人物里，不可靠）
	var corners [4][3]int
	ci := 0
	for _, cx := range []int{bounds.Min.X, bounds.Max.X - 9} {
		for _, cy := range []int{bounds.Min.Y, bounds.Max.Y - 9} {
			var r, g, b int
			for dy := 0; dy < 8; dy++ {
				for dx := 0; dx < 8; dx++ {
					pr, pg, pb, _ := src.At(cx+dx, cy+dy).RGBA()
					r += int(pr >> 8)
					g += int(pg >> 8)
					b += int(pb >> 8)
				}
			}
			corners[ci] = [3]int{r / 64, g / 64, b / 64}
			ci++
		}
	}
	cornersAgree := func(a, b [3]int) bool {
		return abs(a[0]-b[0]) <= 24 && abs(a[1]-b[1]) <= 24 && abs(a[2]-b[2]) <= 24
	}
	hasBg := cornersAgree(corners[0], corners[1]) && cornersAgree(corners[1], corners[2]) &&
		cornersAgree(corners[2], corners[3])
	bg := corners[0]

	blank := make([]bool, width)
	for x := 0; x < width; x++ {
		samples, maxDiff, minLum, maxLum := 0, 0, 1<<30, 0
		nearBg := true
		prev := -1
		for y := 0; y < height; y += step {
			r, g, b, _ := src.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			r8, g8, b8 := int(r>>8), int(g>>8), int(b>>8)
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
			if lum < minLum {
				minLum = lum
			}
			if lum > maxLum {
				maxLum = lum
			}
			if hasBg {
				dr, dg, db := r8-bg[0], g8-bg[1], b8-bg[2]
				if abs(dr) > 24 || abs(dg) > 24 || abs(db) > 24 {
					nearBg = false
				}
			}
			samples++
			prev = lum
		}
		uniform := samples > 0 && maxDiff <= sheetBlankDiffPerPair
		nearWhite := minLum >= 200 && maxLum-minLum <= 32
		blank[x] = uniform && (nearWhite || (hasBg && nearBg))
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
// 非拼图、URL 引用或其他类型一律返回 (nil, false) 走原参考。
func splitCharacterSheetReference(item protocol.MediaReference) ([]protocol.MediaReference, bool) {
	if item.URL != "" || !strings.HasPrefix(strings.ToLower(item.DataURL), "data:image/") {
		return nil, false
	}
	panels, ok := splitCharacterSheetDataUrl(item.DataURL)
	if !ok {
		return nil, false
	}
	suffixes := []string{"_closeup", "_front"}
	split := make([]protocol.MediaReference, 0, len(panels))
	for index, dataURL := range panels {
		panel := item
		panel.DataURL = dataURL
		panel.Name = strings.TrimSpace(item.Name) + suffixes[index]
		if item.ID != "" {
			panel.ID = item.ID + suffixes[index]
		}
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(decodeDataURLBytes(dataURL))); err == nil {
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
