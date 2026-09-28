package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"infinite-canvas/backend/internal/model"
)

// 脚本节点 → 分镜脚本节点转换工具（POST /api/tools/script-to-storyboard）。
// 源节点文本是 LLM 生成的分镜 JSON（可能带 ```json 围栏和前后说明文字），
// 解析后按 cloud agent 分镜行 schema 校验、归一，写入同画布的新 script 节点。

// scriptToStoryboardFieldAliases 把 LLM 常见分镜字段名归一到 cloud agent 行 schema 白名单；
// 白名单之外的字段（timeRange、characters、styleGuide 等）丢弃，不写入行。
var scriptToStoryboardFieldAliases = map[string]string{
	"description":  "plotDescription",
	"action":       "plotDescription",
	"visualPrompt": "imageGenerationPrompt",
	"videoPrompt":  "videoMotionPrompt",
	"shotType":     "shotSize",
}

func (s *Service) ScriptToStoryboard(userID, canvasID, sourceNodeID string) (map[string]any, error) {
	text, err := s.scriptNodeContent(userID, canvasID, sourceNodeID)
	if err != nil {
		return nil, err
	}
	return s.ScriptToStoryboardText(userID, canvasID, sourceNodeID, text)
}

// scriptNodeContent 读源脚本节点文本（节点 metadata.content），供转换与生成分镜行共用。
func (s *Service) scriptNodeContent(userID, canvasID, sourceNodeID string) (string, error) {
	_, _, source, err := s.canvasDocumentForNode(userID, canvasID, sourceNodeID)
	if err != nil {
		return "", err
	}
	metadata, _ := source["metadata"].(map[string]any)
	if metadata == nil {
		return "", nil
	}
	return stringValue(metadata["content"]), nil
}

// canvasDocumentForNode 加载画布文档并确认源节点存在，返回画布记录、文档与源节点。
func (s *Service) canvasDocumentForNode(userID, canvasID, sourceNodeID string) (*model.CanvasProject, map[string]any, map[string]any, error) {
	canvas, err := s.repo.CanvasProjectForUser(userID, canvasID)
	if err != nil {
		return nil, nil, nil, err
	}
	doc, err := creationDocument(canvas.PayloadJSON)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, node := range creationMaps(doc["nodes"]) {
		if stringValue(node["id"]) == sourceNodeID {
			return canvas, doc, node, nil
		}
	}
	return nil, nil, nil, NotFound("未找到源节点")
}

// ScriptToStoryboardText 是转换核心：把给定文本按分镜行 schema 宽松解析、归一，
// 在源节点右侧落成新分镜行节点并保存画布。转换工具与生成分镜行工具共用；
// 文本为空或没有可解析行时报错，由调用方决定错误口径。
func (s *Service) ScriptToStoryboardText(userID, canvasID, sourceNodeID, text string) (map[string]any, error) {
	canvas, doc, source, err := s.canvasDocumentForNode(userID, canvasID, sourceNodeID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" {
		return nil, BadAuthRequest("源节点没有文本内容")
	}
	rows, dropped, err := parseScriptStoryboardRows(text)
	if err != nil {
		return nil, NewAppError(http.StatusUnprocessableEntity, err.Error())
	}
	nodeID := "script-" + newID()
	normalized, err := normalizeScriptStoryboardRows(rows, userID, nodeID)
	if err != nil {
		return nil, err
	}

	// 与前端一致的摆放：源节点右侧 x + width + 96，同一行。
	position, _ := source["position"].(map[string]any)
	x, _ := position["x"].(float64)
	y, _ := position["y"].(float64)
	width, _ := source["width"].(float64)
	if width == 0 {
		width = 340
	}
	x += width + 96
	title := fmt.Sprintf("分镜行（来源：%s）", defaultString(stringValue(source["title"]), "未命名脚本"))
	node := creationAddedNode(CreationCanvasOp{
		Type: "add_node", ID: nodeID, NodeType: "script", Title: title, X: &x, Y: &y,
		Metadata: map[string]any{"content": "", "status": "idle", "workflowKind": "script", "storyboard": map[string]any{
			"rows":             normalized,
			"visibleColumns":   []any{"shotNumber", "durationSeconds", "videoMotionPrompt", "dialogue", "assets"},
			"referenceNodeIds": []any{},
		}},
	})
	doc["nodes"] = append(creationMaps(doc["nodes"]), node)
	policy, err := s.RuntimePolicy()
	if err != nil {
		return nil, err
	}
	if err := saveCloudAgentDocument(s.repo, canvas, doc, policy); err != nil {
		return nil, err
	}
	return map[string]any{"nodeId": nodeID, "rows": len(normalized), "dropped": dropped}, nil
}

// parseScriptStoryboardRows 从脚本文本里宽松抽出分镜行：跳过围栏和前后说明文字，
// 接受 {"shots":[...]} 或裸数组；单行字段名按别名归一后校验，
// 超过一半无效整体拒绝（422），否则丢弃无效行继续。
func parseScriptStoryboardRows(text string) ([]map[string]any, int, error) {
	jsonText, err := extractJSONText(text)
	if err != nil {
		return nil, 0, fmt.Errorf("脚本内容里没有可解析的 JSON")
	}
	var parsed any
	if err := json.Unmarshal([]byte(jsonText), &parsed); err != nil {
		return nil, 0, fmt.Errorf("脚本内容 JSON 解析失败：%v", err)
	}
	var raw []any
	switch value := parsed.(type) {
	case []any:
		raw = value
	case map[string]any:
		raw, _ = value["shots"].([]any)
	}
	if len(raw) == 0 {
		return nil, 0, fmt.Errorf("脚本内容里没有 shots 分镜行")
	}
	valid := make([]map[string]any, 0, len(raw))
	failures := make([]string, 0)
	for index, item := range raw {
		row, _ := item.(map[string]any)
		coerced := scriptToStoryboardRow(row)
		if err := validateCloudAgentStoryboardRow(coerced, false); err != nil {
			failures = append(failures, fmt.Sprintf("镜头 %d：%s", index+1, err.Error()))
			continue
		}
		valid = append(valid, coerced)
	}
	if len(failures)*2 > len(raw) {
		if len(failures) > 3 {
			failures = failures[:3]
		}
		return nil, 0, fmt.Errorf("分镜行有效率不足一半（%d/%d 有效）：%s", len(raw)-len(failures), len(raw), strings.Join(failures, "；"))
	}
	return valid, len(raw) - len(valid), nil
}

// scriptToStoryboardRow 只保留 schema 白名单字段（含别名归一），其余丢弃；
// durationSeconds 缺失时从 timeRange（M:SS–M:SS）或 duration/seconds 推导。
func scriptToStoryboardRow(input map[string]any) map[string]any {
	row := map[string]any{}
	for key, value := range input {
		if alias, ok := scriptToStoryboardFieldAliases[key]; ok {
			row[alias] = value
			continue
		}
		if key == "durationSeconds" {
			// 数字直接保留；字符串（如 "6"）留给 scriptStoryboardSeconds 归一。
			if _, ok := value.(float64); ok {
				row[key] = value
			}
			continue
		}
		if cloudAgentStoryboardTextField(key) {
			row[key] = value
		}
	}
	if _, ok := row["durationSeconds"]; !ok {
		if seconds, ok := scriptStoryboardSeconds(input); ok {
			row["durationSeconds"] = seconds
		}
	}
	return row
}

func scriptStoryboardSeconds(input map[string]any) (float64, bool) {
	for _, key := range []string{"durationSeconds", "duration", "seconds", "时长"} {
		if value, ok := input[key].(float64); ok && value > 0 {
			return value, true
		}
		if text, ok := input[key].(string); ok {
			if seconds, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(text), "秒"), 64); err == nil && seconds > 0 {
				return seconds, true
			}
		}
	}
	if text, ok := input["timeRange"].(string); ok {
		if start, end, ok := parseScriptTimeRange(text); ok {
			return end - start, true
		}
	}
	return 0, false
}

var scriptTimeRangeSeparator = regexp.MustCompile(`\s*[-–—~]\s*`)

func parseScriptTimeRange(text string) (float64, float64, bool) {
	parts := scriptTimeRangeSeparator.Split(strings.TrimSpace(text), 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	start, okStart := parseScriptClock(parts[0])
	end, okEnd := parseScriptClock(parts[1])
	if !okStart || !okEnd || end <= start {
		return 0, 0, false
	}
	return start, end, true
}

func parseScriptClock(text string) (float64, bool) {
	segments := strings.Split(strings.TrimSpace(text), ":")
	total := 0.0
	for _, segment := range segments {
		value, err := strconv.ParseFloat(strings.TrimSpace(segment), 64)
		if err != nil {
			return 0, false
		}
		total = total*60 + value
	}
	return total, true
}

// normalizeScriptStoryboardRows 复刻 normalizeCloudAgentStoryboardRows 的落库形态
// （defaults 合并 + id + shotNumber），但按 requireDescription=false 的口径放行，
// 因此不能直接复用（其内部按 requireDescription=true 整批校验）。
func normalizeScriptStoryboardRows(rows []map[string]any, userID, nodeID string) ([]any, error) {
	if len(rows) < 1 || len(rows) > maxCloudAgentStoryboardRows {
		return nil, BadAuthRequest("有效分镜行必须包含 1 到 100 个镜头")
	}
	seed := newID()
	out := make([]any, 0, len(rows))
	for index, input := range rows {
		row := cloudAgentStoryboardRowDefaults()
		for key, value := range input {
			row[key] = value
		}
		row["id"] = cloudAgentID(userID, fmt.Sprintf("storyboard:%s:%s:%d", nodeID, seed, index+1))
		row["shotNumber"] = float64(index + 1)
		out = append(out, row)
	}
	return out, nil
}
