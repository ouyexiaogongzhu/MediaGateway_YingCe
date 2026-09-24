package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"infinite-canvas/backend/internal/model"
)

// processTask 是任务执行阶段唯一的类型分派入口。
// 任务已经在 CreateTask 阶段完成 admission；这里不把无效视频任务降级成内部工作流成功。
func (s *Service) processTask(ctx context.Context, task model.Task) (map[string]interface{}, []map[string]interface{}, error) {
	if err := validateTaskType(task.Type); err != nil {
		return nil, nil, err
	}
	decryptedInput, err := s.decryptTaskInputJSON(task.InputJSON)
	if err != nil {
		return nil, nil, err
	}
	task.InputJSON = decryptedInput
	ctx = withTaskExecutionID(ctx, task.ID)
	ctx = withProviderAnalytics(ctx, s, task)
	ctx = context.WithValue(ctx, mediaExecutionTaskKey{}, task)

	// TODO(merge): storyboard 分镜执行分支为 fork 独有，实现位于 internal/service/
	// （storyboard_workflow.go / storyboard_row_video.go / storyboard_batch.go），
	// 迁入 internal/app 后这段派发才会编译通过；上游无此分支。
	if task.Type == "agent_storyboard_rows" {
		return s.processStoryboardRowsTask(ctx, task)
	}
	if task.Type == "storyboard_row_video" {
		return s.processStoryboardRowVideoTask(ctx, task)
	}
	if task.Type == "storyboard_video_batch" {
		return s.processStoryboardVideoBatchTask(ctx, task)
	}
	if task.Type == "storyboard_music_batch" {
		return s.processStoryboardMusicBatchTask(ctx, task)
	}
	if task.Type == "storyboard_compose" {
		return s.processStoryboardComposeTask(ctx, task)
	}
	if task.Type == "canvas_text" || task.Type == "canvas_image" || task.Type == "canvas_video" || task.Type == "canvas_audio" {
		result, err := s.processCanvasGenerationTask(ctx, task.UserID, task.ProjectID, task.Type, task.Prompt, task.InputJSON)
		return result, nil, err
	}
	if strings.HasPrefix(task.Type, "video_") {
		if !canRunProviderTask(task) {
			return nil, nil, errors.New("视频任务缺少可执行的模型配置")
		}
		result, err := s.processCanvasGenerationTask(ctx, task.UserID, task.ProjectID, task.Type, task.Prompt, task.InputJSON)
		return result, nil, err
	}
	return nil, nil, errors.New("任务类型没有可用的执行分支")
}

func canRunProviderTask(task model.Task) bool {
	if !strings.HasPrefix(task.Type, "video_") || strings.TrimSpace(task.InputJSON) == "" {
		return false
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(task.InputJSON), &input); err != nil {
		return false
	}
	return hasExecutableProviderVideoConfig(input)
}

func hasExecutableProviderVideoConfig(input map[string]any) bool {
	mode, _ := input["mode"].(string)
	config, ok := input["config"].(map[string]any)
	if mode != "video" || !ok {
		return false
	}
	interfaceType := stringValue(config["interfaceType"])
	if isRunningHubInterface(interfaceType) {
		if stringValue(config["workflowId"]) == "" && stringValue(config["webappId"]) == "" && stringValue(config["model"]) == "" {
			return false
		}
		return stringValue(config["baseUrl"]) != "" && stringValue(config["apiKey"]) != ""
	}
	if stringValue(config["model"]) == "" {
		return false
	}
	return stringValue(config["channelId"]) != "" || (stringValue(config["baseUrl"]) != "" && stringValue(config["apiKey"]) != "")
}
