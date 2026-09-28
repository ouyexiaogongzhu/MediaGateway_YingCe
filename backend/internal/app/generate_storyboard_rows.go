package app

import (
	"context"
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
劇本：`

const storyboardRowsTaskTimeout = 600 * time.Second

func (s *Service) GenerateStoryboardRows(ctx context.Context, userID, canvasID, sourceNodeID, model, logicalModelID string) (map[string]any, error) {
	script, err := s.scriptNodeContent(userID, canvasID, sourceNodeID)
	if err != nil {
		return nil, err
	}
	prompt := StoryboardContractInstruction + "\n\n" + script
	task, err := s.CreateTask(userID, CreateTaskRequest{
		ProjectID:      canvasID,
		Type:           "canvas_text",
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
