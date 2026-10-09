package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"infinite-canvas/backend/internal/model"
)

// 生成分鏰行：把脚本節點的劇本包上实证有效的分镜行契约指令，走脚本生成同一条
// canvas_text 任务执行链（CreateTask → worker → processCanvasGenerationTask），
// 同步等待模型返回后宽松解析 JSON，复用脚本→分镜行转换核心落成新節點。

// StoryboardContractInstruction 实证可产出约 23 镜的完整覆盖分镜行；逐字使用，勿改。
const StoryboardContractInstruction = `將以下劇本拆解為分鏰行。只輸出 JSON 對象（{"shots":[...]}），首字符 { 尾字符 }，禁止任何解釋、前言、Markdown 或散文。
要求：按時間軸連續切分覆蓋全片（每鏡 5–15 秒），shots 數組完整覆蓋到結尾，不得提前收束。
每行欄位：timeRange, shotType, camera, characters, action, dialogue, voiceMode, sfxTags, musicGroupId, visualPrompt, videoPrompt。
輸出前逐對相鄰分鏰核對物理狀態連續性：後一鏡 description 的服裝狀態、姿勢、位置與道具必須承接前一鏡 continuityOut 交接（如「A的手移向B腰側」）所隱含的結果，並持續為每鏡填寫 continuityOut 作為下一鏡的交接依據。
發現矛盾時修正較後一鏡的 description 使其承接前一鏡，不得憑空新增道具或讓角色瞬移。
劇本：`

const storyboardRowsTaskTimeout = 1800 * time.Second

// storyboardRowDefaultDurationSeconds 宿主兜底时长（秒）：模型行缺 durationSeconds 且
// timeRange 不可解析时使用，与前端新建行默认值一致（canvas-script-node 新增行 6s）。
const storyboardRowDefaultDurationSeconds = 6.0

// runStoryboardTextTask 是 canvas_text + operation=storyboard 的正文执行包装：
// 输出抽不出 ≥1 条分镜行时带着错误反馈后缀重试（共 3 次尝试），全部失败才把
// 散文正文原样透传并打 storyboardParseFailed 标记，供前端提示格式降级。
func runStoryboardTextTask(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	const maxAttempts = 3
	basePrompt := input.Prompt
	for attempt := 1; ; attempt++ {
		input.Prompt = basePrompt
		if attempt > 1 {
			input.Prompt += storyboardRetrySuffix
		}
		result, err := runTextTask(ctx, input)
		if err != nil {
			return nil, err
		}
		text, _ := result["text"].(string)
		if !shouldRetryStoryboardOutput(text) {
			normalizeStoryboardTaskText(result, text)
			return result, nil
		}
		if attempt >= maxAttempts {
			if strings.TrimSpace(text) == "" {
				return nil, errors.New("文本模型连续三次没有返回正文")
			}
			result["storyboardParseFailed"] = true
			return result, nil
		}
	}
}

// storyboardRetrySuffix 重试时追加的错误反馈，风格与受保护契约一致；逐字使用，勿改。
const storyboardRetrySuffix = "\n\n【上一次輸出不符合格式】你上次返回了散文而不是 JSON 分鏰行，已被系統拒收。這一次必須只輸出符合 storyboard-plan/v3 Schema 的單個 JSON 對象（{\"shots\":[...]}），第一個字符必須是 {，最後一個字符必須是 }，禁止任何解釋、前言、Markdown 代碼塊或散文。"

// normalizeStoryboardTaskText 把模型侧 storyboard-plan 输出（shots/description/videoPrompt/
// characterIds/shotType…）原地归一成前端 storyboardRowsFromTask 消费的形状
// （rows/plotDescription/videoMotionPrompt/characters…），复用 scriptToStoryboardRow 别名表。
// 画布分镜原生流在浏览器端解析任务正文，此前只认 rows 键且无别名映射，
// 导致「視頻提示詞全是空的」「鏡頭沒有關聯資產（characters 丢失）」。
// 归一失败时保留原文不动（前端已兼容 shots 键，双保险）。
func normalizeStoryboardTaskText(result map[string]interface{}, text string) {
	jsonText, err := extractPreferredJSONText(text, "shots")
	if err != nil {
		if jsonText, err = extractJSONText(text); err != nil {
			return
		}
	}
	var plan struct {
		Title string           `json:"title"`
		Shots []map[string]any `json:"shots"`
		Rows  []map[string]any `json:"rows"`
	}
	if json.Unmarshal([]byte(jsonText), &plan) != nil || len(plan.Shots)+len(plan.Rows) == 0 {
		return
	}
	rows := plan.Shots
	if len(rows) == 0 {
		rows = plan.Rows
	}
	normalized := make([]map[string]any, 0, len(rows))
	// 宿主拥有结构字段：shotNumber 一律按保留行序由宿主赋值，绝不读模型值（模型自编
	// 号曾与前端键位失配造成静默重排；模型 shotNumber 已被 scriptToStoryboardRow 白名单
	// 丢弃）；durationSeconds 缺失或非正数时宿主从 timeRange 推导，仍无则取项目默认时长。
	shotNumber := 0.0
	for _, raw := range rows {
		if raw == nil {
			continue
		}
		shotNumber++
		row := scriptToStoryboardRow(raw)
		row["shotNumber"] = shotNumber
		if seconds, ok := row["durationSeconds"].(float64); !ok || seconds <= 0 {
			delete(row, "durationSeconds")
			if derived, ok := scriptStoryboardSeconds(raw); ok {
				row["durationSeconds"] = derived
			} else {
				row["durationSeconds"] = storyboardRowDefaultDurationSeconds
			}
		}
		// assetRefs 无法安全映射成前端 StoryboardAssetBinding（{nodeId,role,priority}，
		// nodeId 需查资产库），以原字段名透传保数据不丢；前端暂不读，映射缺口待补。
		for _, key := range []string{"sfxTags", "mustHave", "optionalDetails", "voiceMode", "musicGroupId", "musicMood", "assetRefs"} {
			if _, ok := row[key]; ok {
				continue
			}
			if value, ok := raw[key]; ok && value != nil {
				row[key] = value
			}
		}
		if _, ok := row["characters"]; !ok {
			if value, ok := raw["characters"]; ok && value != nil {
				row["characters"] = value
			} else if value, ok := raw["characterIds"]; ok && value != nil {
				row["characters"] = storyboardCharacterRefs(value)
			}
		}
		normalized = append(normalized, row)
	}
	out := map[string]any{"rows": normalized}
	// 行出生即 H3 格式（videoPrompt 规则已注入 storyboard_plan 受保护契约），打标记供
	// 下游/前端识别格式代次；归一失败时不打（走旧格式路径）。
	out["h3Formatted"] = true
	if strings.TrimSpace(plan.Title) != "" {
		out["title"] = plan.Title
	}
	if data, err := json.Marshal(out); err == nil {
		result["text"] = string(data)
	}
}

// storyboardCharacterRefs 把模型输出的 characterIds 字符串数组包成前端
// StoryboardCharacterReference 兼容形状（{"characterName": s}），与前端防御性
// 映射对齐；已是对象的元素原样保留，非数组输入不动。
func storyboardCharacterRefs(value any) any {
	ids, ok := value.([]any)
	if !ok {
		return value
	}
	refs := make([]any, 0, len(ids))
	for _, item := range ids {
		if name, isStr := item.(string); isStr {
			refs = append(refs, map[string]any{"characterName": name})
			continue
		}
		refs = append(refs, item)
	}
	return refs
}

// shouldRetryStoryboardOutput 判定模型输出是否缺少可解析的分镜行（true = 需要重试）。
// 只有产出 ZERO 条可解析行才重试（分层修复：缺 shotNumber/缺 dialogue/缺时长都不是
// 缺陷——宿主补齐，见 normalizeStoryboardTaskText）；行内容结构漂移（如 title/logline
// 开头的异形 JSON）解析不出 shots/rows 时同样视为零行。宽松成功标准：正文能抽出
// JSON，且为含 ≥1 个元素的 shots/rows 数组的对象，或本身就是 ≥1 个元素的裸数组。
func shouldRetryStoryboardOutput(text string) bool {
	jsonText, err := extractPreferredJSONText(text, "shots")
	if err != nil {
		jsonText, err = extractJSONText(text)
		if err != nil {
			return true
		}
	}
	var plan struct {
		Shots []json.RawMessage `json:"shots"`
		Rows  []json.RawMessage `json:"rows"`
	}
	if json.Unmarshal([]byte(jsonText), &plan) == nil {
		return len(plan.Shots)+len(plan.Rows) == 0
	}
	var rows []json.RawMessage
	if json.Unmarshal([]byte(jsonText), &rows) == nil {
		return len(rows) == 0
	}
	return true
}

func (s *Service) GenerateStoryboardRows(ctx context.Context, userID, canvasID, sourceNodeID, model, logicalModelID string) (map[string]any, error) {
	script, err := s.scriptNodeContent(userID, canvasID, sourceNodeID)
	if err != nil {
		return nil, err
	}
	prompt := StoryboardContractInstruction + "\n\n" + script
	task, err := s.CreateTask(userID, CreateTaskRequest{
		ProjectID:      canvasID,
		Type:           "canvas_text",
		Operation:      "storyboard",
		Prompt:         prompt,
		Model:          model,
		LogicalModelID: logicalModelID,
		Input:          map[string]any{"mode": "text", "prompt": prompt, "config": storyboardRowsModelConfig(model, logicalModelID)},
	})
	if err != nil {
		return nil, err
	}
	text, err := s.awaitCanvasTextTaskText(ctx, userID, task.ID, storyboardRowsTaskTimeout)
	if err != nil {
		return nil, err
	}
	return s.ScriptToStoryboardText(userID, canvasID, sourceNodeID, text)
}

// storyboardRowsModelConfig 与画布文本生成同构：前台模型模式 config 不带 channelId
// （admission 才会走 logicalModelId 目录路由），系统渠道模式把前端
// 「channelId::modelKey」编码拆开交给校验；两者皆空时报「缺少模型配置」。
func storyboardRowsModelConfig(model, logicalModelID string) map[string]any {
	if strings.TrimSpace(logicalModelID) != "" {
		return map[string]any{}
	}
	if index := strings.Index(model, "::"); index >= 0 {
		return map[string]any{"channelId": model[:index], "model": model[index+2:]}
	}
	return map[string]any{}
}

// awaitCanvasTextTaskText 轮询任务直到终态，取回正文。超时或客户端断开时任务继续在
// 后台执行，不主动取消——与前端任务中心的可见性一致。
func (s *Service) awaitCanvasTextTaskText(ctx context.Context, userID, taskID string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		task, err := s.repo.TaskForUser(userID, taskID)
		if err != nil {
			return "", err
		}
		switch task.Status {
		case model.TaskStatusSucceeded:
			text := strings.TrimSpace(taskResultText(task.ResultJSON))
			if text == "" {
				return "", errors.New("文本模型没有返回正文")
			}
			return text, nil
		case model.TaskStatusFailed:
			message := strings.TrimSpace(task.Error)
			if message == "" {
				message = "文本生成任务失败"
			}
			return "", errors.New(message)
		case model.TaskStatusCancelled:
			return "", errors.New("文本生成任务已取消")
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("生成分鏰行超时（%d 秒），任务仍在后台执行，可稍后在任务中心查看", int(timeout.Seconds()))
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
