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
