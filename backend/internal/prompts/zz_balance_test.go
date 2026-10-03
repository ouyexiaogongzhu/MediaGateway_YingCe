package prompts

import (
	"encoding/json"
	"testing"
)

func TestBalanceJSONClosersMissingRootBrace(t *testing.T) {
	// 实锤案例形状：22 链输出掉最后根 }
	candidate := `{"title":"x","shots":[{"a":1},{"b":2}]`
	repaired, ok := balanceJSONClosers(candidate)
	if !ok {
		t.Fatal("应当修复")
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(repaired), &decoded); err != nil {
		t.Fatalf("修复后仍非法: %v", err)
	}
	if len(decoded["shots"].([]any)) != 2 {
		t.Fatal("数据丢失")
	}
}

func TestBalanceJSONClosersRejectsTruncation(t *testing.T) {
	if _, ok := balanceJSONClosers(`{"a":"未闭合字符串`); ok {
		t.Fatal("字符串未闭合（真截断）不得修复")
	}
	if _, ok := balanceJSONClosers(`{"a":1}`); ok {
		t.Fatal("完整 JSON 无需修复")
	}
	if _, ok := balanceJSONClosers(`{"a":1]]`); ok {
		t.Fatal("括交错不得修复")
	}
}

func TestExtractJSONTextRepairsMissingCloser(t *testing.T) {
	raw := "前言\n{\"title\":\"x\",\"shots\":[{\"durationSeconds\":5}]"
	got, err := extractJSONText(raw)
	if err != nil {
		t.Fatalf("extract 失败: %v", err)
	}
	var decoded struct {
		Shots []map[string]any `json:"shots"`
	}
	if err := json.Unmarshal([]byte(got), &decoded); err != nil || len(decoded.Shots) != 1 {
		t.Fatalf("修复提取失败: %v %q", err, got)
	}
}
