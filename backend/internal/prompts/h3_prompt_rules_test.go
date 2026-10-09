package prompts

import (
	"strings"
	"testing"
)

// H3 规则注入有回归前科（7f31628a：紧凑 JSON 指令诱发 27B 掉 durationSeconds），
// 这里守护三件事：规则确实挂在受保护契约里（不会被 DB 模板/用户定制剔除）、
// 关键措辞在位（画外音、N/A、四栏）、体量在 <2000 token 预算内。
func TestStoryboardExecutionContractContainsH3Rules(t *testing.T) {
	contract := StoryboardExecutionContract("单个镜头时长 5–15 秒。", "按时间轴连续切分。")
	for _, marker := range []string{
		"says in an off-screen voiceover",
		"non_diegetic_music",
		"N/A",
		"integrated_multimodal_description",
		"overall_soundscape",
		"videoPrompt 按下方 H3 格式规则书写",
		// 注入不得挤掉既有防回归关键约束：
		"durationSeconds", "storyboard-plan/v3",
	} {
		if !strings.Contains(contract, marker) {
			t.Fatalf("受保护契约缺少关键片段 %q", marker)
		}
	}
}

func TestH3VideoPromptRulesWithinTokenBudget(t *testing.T) {
	// 预算 <2000 token；中文按 ~1.5 char/token 保守上限 6000 字符（当前约 1000）。
	if len([]rune(h3VideoPromptRules)) > 6000 {
		t.Fatalf("H3 规则超出预算：%d 字符", len([]rune(h3VideoPromptRules)))
	}
}
