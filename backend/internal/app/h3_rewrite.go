package app

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"time"
)

// canvas_video 提交 Gateway 前的 H3 视频提示词改写管线（创作页等非分鏰路径的手打 prompt）。
//
// 行为契约（G1 实验 /tmp/h3gate/report.md，2026-10-10）：
//   - 任务 metadata 无 h3Formatted=true 时，先经 uncensored 文本模型按 H3 骨架改写再提交；
//   - 改写输出经 span 机械验证（<d> 台词块与 @图片N 逐字保真且数量一致）；
//   - 任何失败（模型不可用 / 超时 / 验证不过）一律降级直通原始 prompt，永不拦渲染。
//
// G1 结论：Run 2 澄清版指令实测 PASS（span 逐字、内容 8/8 短语存活）；「只补骨架」式
// 简短指令实测会把模型退化成摘要模板（Run 1 FAIL），故指令文本逐字取 Run 2，勿精简。

const h3RewriteTimeout = 90 * time.Second

// h3RewriteDefaultModel 默认 uncensored 改写模型（Gateway compat_chat 路由，omlx 按需拉起）。
const h3RewriteDefaultModel = "qwen3.8-uncensored"

// h3RewriteInstructions 逐字取自 G1 Run 2 澄清版指令（PASS 样本）；末尾追加 base-en.txt
// 的骨架词汇约定（四栏、主运镜固定词汇、画外音措辞、N/A），与分鏰模板注入同一权威来源。
const h3RewriteInstructions = `你是 H3 视频提示词改写器。输出=输入提示词的完整改写版（全文，不是骨架、不是摘要、不是模板）。规则：1) 保留输入的每一项内容：所有动作步骤、表情、细节、声音、资源对应关系区块逐字保留，只允许补充 H3 格式骨架（构图/机位/光影/声音）。2) 禁止增删改任何内容细节。3) <d>...</d> 台词块与 @图片N 标记逐字保留，一字不改。4) 直接输出改写后的提示词全文，不要任何解释。
【H3 骨架约定（用英文融入正文，只补不删）】
- 构图：景别、主体左右位置与视觉焦点；光影：光源方向、质感与氛围。
- 机位：只保留一个主运镜，固定词汇 Push In / Pull Out、Pan Left / Pan Right、Truck Left / Truck Right、Tilt Up / Tilt Down、Zoom In / Zoom Out、Arc Shot、Tracking Shot、Static Shot。
- 声音：画面内音效与非语言人声，不重复台词；无配乐写 N/A。
- 台词：说话者首次出现时在 <d> 外标注身份与稳定编号（如 The young woman with a quiet, breathy voice (S1) says: <d>台词原文</d>）；画外音固定写 says in an off-screen voiceover: <d>…</d>。`

// h3FormattedSignature 已按 base-en.txt 三段骨架写就的 prompt 特征（分镜行受保护契约的
// videoPrompt 产物）。行 prompt 目前只在分镜文字任务 result_json 顶层带 h3Formatted 标记，
// 不落画布行节点、前端提交 canvas_video 也不透传 ⇒ 元数据判准存在缺口。内容签名是
// 不依赖任何一方的兜底：手写创作页 prompt 几乎不可能含这些字段名，含了就说明已是 H3
// 格式，跳过改写同样正确（避免对好 prompt 二次加工）。
var h3FormattedSignature = regexp.MustCompile(`integrated_multimodal_description|overall_soundscape|non_diegetic_music|\[Shot \d+\]`)

// canvasH3RewriteEnabled G1=PASS ⇒ 默认开启；显式设 0/false/off 才关闭。
func canvasH3RewriteEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CANVAS_H3_REWRITE_ENABLED"))) {
	case "", "1", "true", "on", "yes":
		return true
	default:
		return false
	}
}

// h3ProtectedSpanPatterns 改写前后必须逐字保真的 span：<d> 台词块与 @图片N 资源标记。
var h3ProtectedSpanPatterns = []*regexp.Regexp{
	regexp.MustCompile(`<d\b[^>]*>[\s\S]*?</d>`),
	regexp.MustCompile(`@图片\d+`),
}

func h3ExtractProtectedSpans(prompt string, patterns []*regexp.Regexp) []string {
	var spans []string
	for _, re := range patterns {
		spans = append(spans, re.FindAllString(prompt, -1)...)
	}
	return spans
}

// h3VerifySpanPreservation 机械验证：改写输出必须逐字包含原 prompt 的每一个受保护 span，
// 且每种 pattern 命中数量与原文完全一致（防丢失也防复制膨胀）。原文无 span 时恒通过。
func h3VerifySpanPreservation(original, rewritten string, patterns []*regexp.Regexp) (bool, string) {
	if strings.TrimSpace(rewritten) == "" {
		return false, "empty_rewrite"
	}
	for _, span := range h3ExtractProtectedSpans(original, patterns) {
		if !strings.Contains(rewritten, span) {
			return false, "span_dropped"
		}
	}
	for _, re := range patterns {
		if len(re.FindAllString(rewritten, -1)) != len(re.FindAllString(original, -1)) {
			return false, "span_count_mismatch:" + re.String()
		}
	}
	return true, ""
}

// canvasH3RewriteModelName 改写目标模型名，可用环境变量覆盖（渠道里按模型 key 匹配）。
func canvasH3RewriteModelName() string {
	if name := strings.TrimSpace(os.Getenv("CANVAS_H3_REWRITE_MODEL")); name != "" {
		return name
	}
	return h3RewriteDefaultModel
}

// h3RewriteChatConfig 在已启用的系统渠道里找文本能力的改写模型，解析成可直接调用的 config。
func (s *Service) h3RewriteChatConfig() (providerConfig, error) {
	// 部分单元测试直接构造 Service 字面量（repo 为 nil）；改写是锦上添花，缺仓储直接降级。
	if s == nil || s.repo == nil {
		return providerConfig{}, errors.New("服务仓储未初始化")
	}
	target := canvasH3RewriteModelName()
	channels, err := s.repo.SystemChannels(false)
	if err != nil {
		return providerConfig{}, err
	}
	for _, channel := range channels {
		models, listErr := s.repo.ChannelModels(channel.ID, false)
		if listErr != nil {
			continue
		}
		for _, item := range models {
			if item.Capability != "text" {
				continue
			}
			if !strings.EqualFold(item.ModelKey, target) && !strings.EqualFold(item.ProviderModelKey, target) {
				continue
			}
			return s.resolveProviderConfig(providerConfig{ChannelID: channel.ID, ChannelModelKey: item.ModelKey})
		}
	}
	return providerConfig{}, errors.New("没有已启用的渠道提供模型 " + target)
}

// h3RewriteResolveConfig / h3RewriteChatCall 提出为包级变量，单测用 stub 替换，不真打 LLM。
var h3RewriteResolveConfig = func(s *Service) (providerConfig, error) {
	return s.h3RewriteChatConfig()
}

var h3RewriteChatCall = func(ctx context.Context, config providerConfig, prompt string) (string, error) {
	out, err := runTextTask(ctx, canvasGenerationInput{Mode: "text", Prompt: prompt, Config: config})
	if err != nil {
		return "", err
	}
	text, _ := out["text"].(string)
	if strings.TrimSpace(text) == "" {
		return "", errors.New("改写模型未返回正文")
	}
	return text, nil
}

// maybeRewriteVideoPromptH3 返回 (改写结果, "") 表示改写成功可用；( "", 原因 ) 表示降级
// 直通原始 prompt；( "", "" ) 表示本次无需处理（开关关闭 / 已是 H3 格式 / 恢复续跑）。
func (s *Service) maybeRewriteVideoPromptH3(ctx context.Context, prompt string, metadata map[string]interface{}) (string, string) {
	if !canvasH3RewriteEnabled() {
		return "", ""
	}
	if metadataTruthy(metadata, "h3Formatted") {
		return "", ""
	}
	if h3FormattedSignature.MatchString(prompt) {
		return "", ""
	}
	if resumedProviderRequestID(ctx) != "" {
		// 恢复/轮询续跑：上游任务已按原始 prompt 提交，改写既浪费 90s 也可能换词。
		return "", ""
	}
	config, err := h3RewriteResolveConfig(s)
	if err != nil {
		return "", "no_rewrite_model:" + err.Error()
	}
	callCtx, cancel := context.WithTimeout(ctx, h3RewriteTimeout)
	defer cancel()
	rewritten, err := h3RewriteChatCall(callCtx, config, h3RewriteInstructions+"\n\n【输入提示词】\n"+prompt)
	if err != nil {
		return "", "rewrite_failed:" + truncateRunes(err.Error(), 200)
	}
	if ok, reason := h3VerifySpanPreservation(prompt, rewritten, h3ProtectedSpanPatterns); !ok {
		return "", "span_verification_failed:" + reason
	}
	return strings.TrimSpace(rewritten), ""
}

// metadataTruthy 兼容 bool / 字符串 / JSON 数字三种写法。
func metadataTruthy(metadata map[string]interface{}, key string) bool {
	switch value := metadata[key].(type) {
	case bool:
		return value
	case string:
		normalized := strings.ToLower(strings.TrimSpace(value))
		return normalized == "true" || normalized == "1"
	case float64:
		return value != 0
	default:
		return false
	}
}
