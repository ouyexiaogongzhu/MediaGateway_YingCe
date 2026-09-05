package service

import (
	"reflect"
	"strings"
	"testing"
)

func TestStoryboardAudioFallback(t *testing.T) {
	cases := []struct {
		name            string
		dialogue        string
		charactersCount int
		audioEffects    string
		wantVoiceMode   string
		wantSfxTags     []string
		wantMusicGroup  string
		wantMusicMood   string
	}{
		{
			name:            "对白有角色",
			dialogue:        "你终于来了。",
			charactersCount: 1,
			audioEffects:    "",
			wantVoiceMode:   "dialogue",
			wantSfxTags:     []string{},
			wantMusicGroup:  "seg-01",
			wantMusicMood:   "",
		},
		{
			name:            "对白无角色视为旁白",
			dialogue:        "那年冬天格外漫长。",
			charactersCount: 0,
			audioEffects:    "",
			wantVoiceMode:   "voiceover",
			wantSfxTags:     []string{},
			wantMusicGroup:  "seg-01",
			wantMusicMood:   "",
		},
		{
			name:            "纯旁白带环境音",
			dialogue:        "雨夜里，无人应答。",
			charactersCount: 0,
			audioEffects:    "雨声淅沥，远处雷鸣",
			wantVoiceMode:   "voiceover",
			wantSfxTags:     []string{"ambience_rain", "thunder"},
			wantMusicGroup:  "seg-01",
			wantMusicMood:   "",
		},
		{
			name:            "无台词有音效描述",
			dialogue:        "",
			charactersCount: 2,
			audioEffects:    "木门吱呀打开，脚步声接近",
			wantVoiceMode:   "dialogue",
			wantSfxTags:     []string{"door_open", "footsteps_stone"},
			wantMusicGroup:  "seg-01",
			wantMusicMood:   "",
		},
		{
			name:            "多关键词按出现顺序且最多三个",
			dialogue:        "拔剑！",
			charactersCount: 1,
			audioEffects:    "寒冰碎裂，魔法汇聚，雷声滚滚，马蹄逼近",
			wantVoiceMode:   "dialogue",
			wantSfxTags:     []string{"ice_crack", "magic_cast", "thunder"},
			wantMusicGroup:  "seg-01",
			wantMusicMood:   "",
		},
		{
			name:            "无关键词返回空标签",
			dialogue:        "沉默对视。",
			charactersCount: 2,
			audioEffects:    "气氛凝重",
			wantVoiceMode:   "dialogue",
			wantSfxTags:     []string{},
			wantMusicGroup:  "seg-01",
			wantMusicMood:   "",
		},
		{
			name:            "同标签去重",
			dialogue:        "海浪拍岸。",
			charactersCount: 0,
			audioEffects:    "浪花与水声交织",
			wantVoiceMode:   "voiceover",
			wantSfxTags:     []string{"ambience_water"},
			wantMusicGroup:  "seg-01",
			wantMusicMood:   "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeStoryboardAudioFallback(tc.dialogue, tc.charactersCount, tc.audioEffects)
			if got.VoiceMode != tc.wantVoiceMode {
				t.Errorf("voiceMode = %q, want %q", got.VoiceMode, tc.wantVoiceMode)
			}
			if !reflect.DeepEqual(got.SfxTags, tc.wantSfxTags) {
				t.Errorf("sfxTags = %v, want %v", got.SfxTags, tc.wantSfxTags)
			}
			if got.MusicGroupID != tc.wantMusicGroup {
				t.Errorf("musicGroupId = %q, want %q", got.MusicGroupID, tc.wantMusicGroup)
			}
			if got.MusicMood != tc.wantMusicMood {
				t.Errorf("musicMood = %q, want %q", got.MusicMood, tc.wantMusicMood)
			}
		})
	}
}

func TestStoryboardPlanContractRequiresAudioFields(t *testing.T) {
	schema := storyboardPlanJSONSchema
	for _, field := range []string{"voiceMode", "sfxTags", "musicGroupId", "musicMood"} {
		if !strings.Contains(schema, `"`+field+`"`) {
			t.Errorf("storyboard 契约 schema 缺少字段 %s", field)
		}
	}
	quoted := `"voiceMode", "sfxTags", "musicGroupId", "musicMood"`
	if !strings.Contains(schema, quoted) {
		t.Errorf("storyboard 契约 required 列表未包含 %s", quoted)
	}
}
