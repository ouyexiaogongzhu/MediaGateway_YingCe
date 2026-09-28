package app

import (
	"encoding/json"
	"strings"
	"testing"

	"infinite-canvas/backend/internal/model"
)

func scriptToStoryboardFixture(t *testing.T, content string) (*Service, string) {
	t.Helper()
	s, db, _, _ := creationTestService(t)
	doc := map[string]any{"nodes": []any{map[string]any{
		"id": "text-1", "type": "text", "title": "测试脚本", "position": map[string]any{"x": 100.0, "y": 50.0}, "width": 360.0, "height": 240.0,
		"metadata": map[string]any{"content": content, "status": "idle"},
	}}, "connections": []any{}}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	canvas := &model.CanvasProject{ID: "s2s-canvas", UserID: "user", Title: "转换测试", PayloadJSON: string(raw)}
	if err := db.Create(canvas).Error; err != nil {
		t.Fatal(err)
	}
	return s, canvas.ID
}

func scriptToStoryboardShotsText() string {
	return "```json\n" + `{"shots":[
		{"timeRange":"0:00-0:06","shotType":"Wide Shot","camera":"长焦推镜","action":"火法师与冰法师雪原对峙","dialogue":"","visualPrompt":"雪原全景对峙","videoPrompt":"镜头缓慢推进","characters":[{"characterName":"烬炎"}]},
		{"timeRange":"0:06-0:12","camera":"手持跟拍","description":"主角冲出巷口"},
		{"durationSeconds":"8","dialogue":"收队"},
		{"shotType":"Close Up"}
	]}` + "\n```"
}

func TestScriptToStoryboardCreatesNode(t *testing.T) {
	s, canvasID := scriptToStoryboardFixture(t, scriptToStoryboardShotsText())
	result, err := s.ScriptToStoryboard("user", canvasID, "text-1")
	if err != nil {
		t.Fatal(err)
	}
	if result["rows"] != 3 || result["dropped"] != 1 {
		t.Fatalf("unexpected result: %v", result)
	}
	stored, err := s.repo.CanvasProjectForUser("user", canvasID)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := creationDocument(stored.PayloadJSON)
	if err != nil {
		t.Fatal(err)
	}
	var created map[string]any
	for _, node := range creationMaps(doc["nodes"]) {
		if stringValue(node["id"]) == stringValue(result["nodeId"]) {
			created = node
		}
	}
	if created == nil {
		t.Fatal("storyboard node not written")
	}
	if stringValue(created["type"]) != "script" {
		t.Fatalf("node type = %q", stringValue(created["type"]))
	}
	position, _ := created["position"].(map[string]any)
	if x, _ := position["x"].(float64); x != 100+360+96 {
		t.Fatalf("node x = %v", position["x"])
	}
	metadata := created["metadata"].(map[string]any)
	storyboard := metadata["storyboard"].(map[string]any)
	rows := creationMaps(storyboard["rows"])
	if len(rows) != 3 {
		t.Fatalf("stored rows = %d", len(rows))
	}
	first := rows[0]
	// 别名归一 + timeRange 推导时长 + 白名单外字段丢弃。
	if first["plotDescription"] != "火法师与冰法师雪原对峙" {
		t.Fatalf("plotDescription = %v", first["plotDescription"])
	}
	if first["imageGenerationPrompt"] != "雪原全景对峙" || first["videoMotionPrompt"] != "镜头缓慢推进" {
		t.Fatalf("prompt alias failed: %v %v", first["imageGenerationPrompt"], first["videoMotionPrompt"])
	}
	if first["durationSeconds"] != 6.0 {
		t.Fatalf("durationSeconds = %v", first["durationSeconds"])
	}
	// 输入的 characters 数组被丢弃，defaults 落成空数组（与 cloud agent 行形态一致）。
	if characters, ok := first["characters"].([]any); !ok || len(characters) != 0 {
		t.Fatalf("characters = %v", first["characters"])
	}
	if stringValue(first["id"]) == "" {
		t.Fatal("row id missing")
	}
	if second := rows[1]; second["durationSeconds"] != 6.0 || second["plotDescription"] != "主角冲出巷口" {
		t.Fatalf("second row = %v", second)
	}
	// 字符串时长 "8" 归一为数字。
	if third := rows[2]; third["durationSeconds"] != 8.0 || third["dialogue"] != "收队" {
		t.Fatalf("third row = %v", third)
	}
}

func TestScriptToStoryboardRejects(t *testing.T) {
	cases := []struct {
		name    string
		content string
		message string
	}{
		{"no json", "纯文本剧本，没有 JSON", "没有可解析的 JSON"},
		{"no shots", `{"title":"只有标题"}`, "没有 shots 分镜行"},
		{"mostly invalid", `{"shots":[{"camera":"a"},{"camera":"b"},{"camera":"c"}]}`, "有效率不足一半"},
		{"empty", "", "没有文本内容"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			content := item.content
			if item.name == "empty" {
				content = "  "
			}
			s, canvasID := scriptToStoryboardFixture(t, content)
			if _, err := s.ScriptToStoryboard("user", canvasID, "text-1"); err == nil || !strings.Contains(err.Error(), item.message) {
				t.Fatalf("expected %q, got %v", item.message, err)
			}
		})
	}
}

func TestScriptToStoryboardBareArrayAndSourceGuards(t *testing.T) {
	s, canvasID := scriptToStoryboardFixture(t, `前置说明文字 [ {"durationSeconds":5,"dialogue":"开火"} ] 后置说明`)
	result, err := s.ScriptToStoryboard("user", canvasID, "text-1")
	if err != nil {
		t.Fatal(err)
	}
	if result["rows"] != 1 || result["dropped"] != 0 {
		t.Fatalf("unexpected result: %v", result)
	}
	if _, err := s.ScriptToStoryboard("user", canvasID, "missing-node"); err == nil || !strings.Contains(err.Error(), "未找到源节点") {
		t.Fatalf("expected missing node error, got %v", err)
	}
}
