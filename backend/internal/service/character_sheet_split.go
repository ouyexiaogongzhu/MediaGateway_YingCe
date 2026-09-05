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
	sheetMinWidth          = 600  // 拼图必为宽幅横幅
	sheetMinPanelWidth     = 100  // 单格最小宽度
	sheetMinSeparatorWidth = 4    // 格间留白至少这么宽才算分隔线（滤掉面板内部的偶发匀色列）
	sheetBlankDiffPerPair  = 6    // 相邻采样像素灰度差平均值 ≤6 视为留白列
	sheetMinValidPanels    = 3    // 至少 3 格才认定是拼图
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
	appendPanel := func(end int) {
		if end-cursor >= sheetMinPanelWidth && height*10 >= (end-cursor)*11 {
			panels = append(panels, segment{cursor, end})
		}
		cursor = end
	}
	for _, sep := range separators {
		appendPanel(sep.start)
	}
	appendPanel(width)
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

// sheetBlankColumns 逐列采样计算相邻像素灰度差之和，接近匀色的列记为留白列。
func sheetBlankColumns(src image.Image) []bool {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	step := height / 120
	if step < 1 {
		step = 1
	}
	blank := make([]bool, width)
	for x := 0; x < width; x++ {
		diffSum, pairs := 0, 0
		prev := -1
		for y := 0; y < height; y += step {
			r, g, b, _ := src.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			gray := int((r*299 + g*587 + b*114) / 1000 >> 8)
			if prev >= 0 {
				d := gray - prev
				if d < 0 {
					d = -d
				}
				diffSum += d
				pairs++
			}
			prev = gray
		}
		blank[x] = pairs > 0 && diffSum <= pairs*sheetBlankDiffPerPair
	}
	return blank
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
