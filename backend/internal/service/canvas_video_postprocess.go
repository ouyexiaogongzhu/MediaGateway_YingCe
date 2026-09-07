package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"infinite-canvas/backend/internal/model"
)

// canvas_video 音频分工后处理：任务成功落库后异步补做 台词配音 → BGM → 混音。
// 任一步失败不影响视频任务本身（错误写 result.postProcessError，产物仍用原视频）；
// 成功后 mix 产物成为最终产物，原视频保留在 result.audioPostProcess.sourceVideo。

// audioPostProcessResultOffset 给整个后处理链一个总预算（3 个 gateway job 串行 + 余量）。
const audioPostProcessTotalTimeout = 3 * defaultMediaGatewayJobTimeout

// maybeScheduleCanvasVideoAudioPostProcess 判断任务是否需要音频后处理，需要则异步执行。
// 只针对成功落库的 canvas_video 任务；无 dialogue 且无 bgm_prompt 时零开销。
func (s *Service) maybeScheduleCanvasVideoAudioPostProcess(task model.Task, ctx context.Context) {
	if task.Type != "canvas_video" || task.Status != model.TaskStatusSucceeded {
		return
	}
	decrypted, err := s.decryptTaskInputJSON(task.InputJSON)
	if err != nil {
		return
	}
	var input canvasGenerationInput
	if json.Unmarshal([]byte(decrypted), &input) != nil {
		return
	}
	dialogue := strings.TrimSpace(input.Dialogue)
	bgm := strings.TrimSpace(input.BGMPrompt)
	if dialogue == "" && bgm == "" {
		return
	}
	// 防重复触发：结果里已有后处理产物或错误标记就不再跑（worker 与人工恢复两个入口互斥，
	// 这里只兜底异常重入）。
	var result map[string]any
	if json.Unmarshal([]byte(task.ResultJSON), &result) != nil {
		return
	}
	if _, done := result["audioPostProcess"]; done {
		return
	}
	if _, failed := result["postProcessError"]; failed {
		return
	}
	// worker ctx 随任务领取结束被取消；后处理与 renderProjectShot 同样脱离请求生命周期。
	postCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), audioPostProcessTotalTimeout)
	go func() {
		defer cancel()
		s.runCanvasVideoAudioPostProcess(postCtx, task, input, dialogue, bgm)
	}()
}

func (s *Service) runCanvasVideoAudioPostProcess(ctx context.Context, task model.Task, input canvasGenerationInput, dialogue string, bgm string) {
	applyErr := func(err error) {
		if writeErr := s.updateTaskResult(task.ID, task.ResultJSON, func(result map[string]any) {
			result["postProcessError"] = truncateRunes(err.Error(), 500)
		}); writeErr != nil {
			_ = s.log(task.UserID, task.ID, "error", "音频后处理失败且回写错误信息失败", err.Error()+" / "+writeErr.Error())
		} else {
			_ = s.log(task.UserID, task.ID, "warn", "音频后处理失败，产物保持原视频", err.Error())
		}
	}

	videoItem, _ := resultVideoItem(task.ResultJSON)
	if videoItem == nil {
		applyErr(fmt.Errorf("任务结果缺少视频产物，跳过音频后处理"))
		return
	}
	resourceID, _ := videoItem["resourceId"].(string)
	if strings.TrimSpace(resourceID) == "" {
		applyErr(fmt.Errorf("视频产物缺少 resourceId，跳过音频后处理"))
		return
	}
	resource, err := s.repo.ResourceForUser(task.UserID, resourceID)
	if err != nil {
		applyErr(fmt.Errorf("读取视频资源失败：%w", err))
		return
	}
	videoPath, cleanup, err := s.gatewayVideoInputPath(task.UserID, resource)
	if err != nil {
		applyErr(fmt.Errorf("准备混音视频输入失败：%w", err))
		return
	}
	defer cleanup()

	durationSeconds := 0.0
	if resource.DurationMs > 0 {
		durationSeconds = float64(resource.DurationMs) / 1000
	}
	if durationSeconds <= 0 {
		if parsed, parseErr := strconv.ParseFloat(strings.TrimSpace(input.Config.VideoSeconds), 64); parseErr == nil {
			durationSeconds = parsed
		}
	}
	durationMs := resource.DurationMs
	if durationMs <= 0 {
		durationMs = int64(durationSeconds * 1000)
	}

	gateway := s.mediaGateway()
	tracks := make([]map[string]any, 0, 2)
	voiceJobID, musicJobID := "", ""
	if dialogue != "" {
		if voiceKey := s.canvasVideoVoiceKey(task.UserID, input); voiceKey == "" {
			_ = s.log(task.UserID, task.ID, "info", "任务没有角色声音关联，跳过台词配音", "")
		} else if job, err := gateway.createJob(ctx, "voice", map[string]any{"text": dialogue, "voice": voiceKey}); err != nil {
			applyErr(fmt.Errorf("提交配音任务失败：%w", err))
			return
		} else if job, err = gateway.wait(ctx, job.ID); err != nil {
			applyErr(fmt.Errorf("配音生成失败：%w", err))
			return
		} else {
			voiceJobID = job.ID
			tracks = append(tracks, map[string]any{"path": job.OutputPath, "start": 0})
		}
	}
	if bgm != "" {
		musicParams := map[string]any{"prompt": bgm}
		if durationSeconds > 0 {
			musicParams["duration_s"] = durationSeconds
		}
		job, err := gateway.createJob(ctx, "music", musicParams)
		if err != nil {
			applyErr(fmt.Errorf("提交配乐任务失败：%w", err))
			return
		}
		if job, err = gateway.wait(ctx, job.ID); err != nil {
			applyErr(fmt.Errorf("配乐生成失败：%w", err))
			return
		}
		musicJobID = job.ID
		tracks = append(tracks, map[string]any{"path": job.OutputPath, "loop": true})
	}
	if len(tracks) == 0 {
		// 只有对白且没有声音关联：无事可做，保持原样。
		return
	}
	job, err := gateway.createJob(ctx, "mix", map[string]any{"video": videoPath, "audio_tracks": tracks, "output_name": "final.mp4"})
	if err != nil {
		applyErr(fmt.Errorf("提交混音任务失败：%w", err))
		return
	}
	if job, err = gateway.wait(ctx, job.ID); err != nil {
		applyErr(fmt.Errorf("混音失败：%w", err))
		return
	}
	mixOutputPath := firstNonEmpty(strings.TrimSpace(job.OutputPath), gateway.metaString(job, "output_path"))
	if mixOutputPath == "" {
		applyErr(fmt.Errorf("混音 job %s 未返回输出文件", job.ID))
		return
	}
	finalResource, err := s.registerGatewayFileResource(task.UserID, "canvas-video", mixOutputPath, durationMs)
	if err != nil {
		applyErr(fmt.Errorf("混音成片登记素材库失败：%w", err))
		return
	}
	finalVideo := map[string]any{
		"dataUrl":    "/api/resources/" + finalResource.ID + "/file",
		"url":        "/api/resources/" + finalResource.ID + "/file",
		"storageKey": "resource:" + finalResource.ID,
		"resourceId": finalResource.ID,
		"bytes":      finalResource.Size,
		"mimeType":   finalResource.MimeType,
		"width":      finalResource.Width,
		"height":     finalResource.Height,
	}
	if err := s.updateTaskResult(task.ID, task.ResultJSON, func(result map[string]any) {
		result["video"] = finalVideo
		result["audioPostProcess"] = map[string]any{
			"voiceJobId": voiceJobID, "musicJobId": musicJobID, "mixJobId": job.ID,
			"mixOutputPath": mixOutputPath, "sourceVideo": videoItem,
		}
	}); err != nil {
		applyErr(fmt.Errorf("混音成片已生成（%s）但回写任务产物失败：%w", mixOutputPath, err))
		return
	}
	_ = s.log(task.UserID, task.ID, "info", "音频后处理完成，最终产物为混音成片", job.ID)
}

// canvasVideoVoiceKey 从任务 metadata 的角色/资产版本关联反查音色（shotVoiceKey 同一条链）；
// 无关联返回空串，调用方跳过配音。
func (s *Service) canvasVideoVoiceKey(userID string, input canvasGenerationInput) string {
	versionID := firstNonEmpty(
		metadataString(input.Metadata, "assetVersionId"),
		metadataString(input.Metadata, "characterVersionId"),
		metadataString(input.Metadata, "characterAssetVersionId"),
	)
	if strings.TrimSpace(versionID) == "" {
		return ""
	}
	binding, err := s.repo.CharacterVoiceBinding(versionID)
	if err != nil || strings.TrimSpace(binding.VoiceProfileID) == "" {
		return ""
	}
	profile, err := s.repo.VoiceProfileForUser(userID, binding.VoiceProfileID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(profile.VoiceKey)
}

// gatewayVideoInputPath 把视频资源解析成 gateway 可读的本地路径；
// 本地存储直接用文件，远端对象下载到临时文件由 cleanup 删除。
func (s *Service) gatewayVideoInputPath(userID string, resource *model.Resource) (string, func(), error) {
	if resource.Provider == "local" {
		path := filepath.Join(s.dataDir, "resources", filepath.FromSlash(resource.ObjectKey))
		if _, err := os.Stat(path); err == nil {
			return path, func() {}, nil
		}
	}
	path, err := s.downloadResourceToTemp(userID, resource)
	if err != nil {
		return "", func() {}, err
	}
	return path, func() { os.Remove(path) }, nil
}

// resultVideoItem 从任务结果 JSON 取顶层 video 产物对象。
func resultVideoItem(raw string) (map[string]any, error) {
	var result map[string]any
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, err
	}
	item, _ := result["video"].(map[string]any)
	return item, nil
}

// updateTaskResult 读改写已完成任务的 result_json；base 为触发时的结果快照，
// 若任务结果在异步期间被其他写入方改过则以库内最新为准。
func (s *Service) updateTaskResult(taskID string, base string, mutate func(map[string]any)) error {
	latest, err := s.repo.Task(taskID)
	if err != nil {
		return err
	}
	raw := firstNonEmpty(latest.ResultJSON, base)
	var result map[string]any
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return err
	}
	if result == nil {
		result = map[string]any{}
	}
	mutate(result)
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return s.repo.UpdateTaskResultIfSucceeded(taskID, string(encoded))
}
