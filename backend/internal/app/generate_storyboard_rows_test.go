package app

import (
	"reflect"
	"strings"
	"testing"
)

func TestStoryboardRowsModelConfig(t *testing.T) {
	channel := storyboardRowsModelConfig("ch1::gpt-x", "")
	if !reflect.DeepEqual(channel, map[string]any{"channelId": "ch1", "model": "gpt-x"}) {
		t.Fatalf("系统渠道模型应拆成 channelId+model，得到 %v", channel)
	}
	if managed := storyboardRowsModelConfig("ch1::gpt-x", "lm-1"); len(managed) != 0 {
		t.Fatalf("前台模型模式 config 不能带 channelId，得到 %v", managed)
	}
	if bare := storyboardRowsModelConfig("bare-model", ""); len(bare) != 0 {
		t.Fatalf("无渠道编码应留空交给 admission 报错，得到 %v", bare)
	}
	if !strings.Contains(StoryboardContractInstruction, "shots") {
		t.Fatalf("契约指令缺少 shots 输出要求")
	}
}

func TestShouldRetryStoryboardOutput(t *testing.T) {
	oneShot := `{"shots":[{"timeRange":"0:00–0:05","action":"开场"}]}`
	cases := []struct {
		name  string
		text  string
		retry bool
	}{
		{"纯散文", "我針對您的優化版進行了微調與細化，劇本如下……", true},
		{"空输出", "", true},
		{"shots 对象", oneShot, false},
		{"durationSeconds 行（schema 硬約束產出）", `{"shots":[{"durationSeconds":5,"action":"开场"}]}`, false},
		{"混合 duration/timeRange", `[{"durationSeconds":6},{"timeRange":"0:05–0:11"}]`, false},
		{"散文包裹 shots", "好的，以下是分镜：\n" + oneShot + "\n如需調整請告知。", false},
		{"rows 键", `{"rows":[{"timeRange":"0:00–0:05"}]}`, false},
		{"裸数组", `[{"timeRange":"0:00–0:05"}]`, false},
		{"空 shots", `{"shots":[]}`, true},
		{"无关 JSON", `{"note":"这是一段说明"}`, true},
		{"截断 JSON", `{"shots":[{"timeRange":"0:00`, true},
	}
	for _, tc := range cases {
		if got := shouldRetryStoryboardOutput(tc.text); got != tc.retry {
			t.Fatalf("%s: shouldRetryStoryboardOutput = %v, 期望 %v", tc.name, got, tc.retry)
		}
	}
	if !strings.Contains(storyboardRetrySuffix, "storyboard-plan/v3") || !strings.Contains(storyboardRetrySuffix, "第一個字符必須是 {") {
		t.Fatalf("重试后缀缺少关键格式要求：%s", storyboardRetrySuffix)
	}
}
