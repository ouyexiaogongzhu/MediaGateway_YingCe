package service

import (
	"sort"
	"strings"
)

// storyboardAudioFallbackResult 存量分镜行的声音标注补齐结果。
type storyboardAudioFallbackResult struct {
	VoiceMode    string
	SfxTags      []string
	MusicGroupID string
	MusicMood    string
}

// storyboardSfxKeywordMap 把 audioEffects 文本中的关键词映射到冻结音效词表标签。
var storyboardSfxKeywordMap = []struct {
	keyword string
	tag     string
}{
	{"风", "ambience_wind"},
	{"雨", "ambience_rain"},
	{"雪", "ambience_snow"},
	{"火", "ambience_fire"},
	{"水", "ambience_water"},
	{"浪", "ambience_water"},
	{"夜", "ambience_night"},
	{"虫", "ambience_night"},
	{"脚步", "footsteps_stone"},
	{"门", "door_open"},
	{"爆炸", "explosion"},
	{"雷", "thunder"},
	{"剑", "sword_clash"},
	{"魔法", "magic_cast"},
	{"冰", "ice_crack"},
	{"晶", "ice_crack"},
	{"马", "horse"},
	{"铃", "bell"},
}

// computeStoryboardAudioFallback 对声音标注字段为零值的存量分镜行做启发式补齐。
// 输入：dialogue, charactersCount, audioEffects；输出：voiceMode, sfxTags, musicGroupId, musicMood。
func computeStoryboardAudioFallback(dialogue string, charactersCount int, audioEffects string) storyboardAudioFallbackResult {
	return storyboardAudioFallbackResult{
		VoiceMode:    fallbackStoryboardVoiceMode(dialogue, charactersCount),
		SfxTags:      fallbackStoryboardSfxTags(audioEffects),
		MusicGroupID: "seg-01",
		MusicMood:    "",
	}
}

// fallbackStoryboardVoiceMode：dialogue 非空时按有无在场角色区分对白/旁白；
// dialogue 为空时返回 "dialogue" 占位，服务端以 dialogue 为空判断本镜头无声。
func fallbackStoryboardVoiceMode(dialogue string, charactersCount int) string {
	if strings.TrimSpace(dialogue) == "" {
		return "dialogue"
	}
	if charactersCount > 0 {
		return "dialogue"
	}
	return "voiceover"
}

// fallbackStoryboardSfxTags 按关键词在 audioEffects 文本中的出现顺序映射标签，去重后最多 3 个。
// ponytail: 单关键词首个命中即计一次，不做全文多次计数；需要更细粒度时改成正则扫描。
func fallbackStoryboardSfxTags(audioEffects string) []string {
	type hit struct {
		pos int
		tag string
	}
	var hits []hit
	for _, m := range storyboardSfxKeywordMap {
		if pos := strings.Index(audioEffects, m.keyword); pos >= 0 {
			hits = append(hits, hit{pos: pos, tag: m.tag})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].pos < hits[j].pos })
	tags := make([]string, 0, len(hits))
	for _, h := range hits {
		if len(tags) == 3 {
			break
		}
		duplicated := false
		for _, existing := range tags {
			if existing == h.tag {
				duplicated = true
				break
			}
		}
		if !duplicated {
			tags = append(tags, h.tag)
		}
	}
	return tags
}
