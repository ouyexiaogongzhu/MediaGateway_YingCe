package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"infinite-canvas/backend/internal/model"
)

// 多镜头连续渲染编排器：按 Shot.Position 串行渲染项目内所有镜头，
// 上一镜头尾帧（gateway meta.last_frame_path）作为下一镜头首帧，
// 全部完成后 concat 成片并登记进素材库。失败即停；已完成镜头的产物已回写。

type RenderAllShotsRequest struct {
	MusicPrompt string `json:"musicPrompt"`
	FromShotID  string `json:"fromShotId"`
	// Draft：纯 h3 快速草稿（跳过 image/voice/music/混音，512x288），确认构图后再走完整链
	Draft bool `json:"draft"`
}

type RenderAllShotsResult struct {
	ProjectID           string               `json:"projectId"`
	Shots               []RenderedShotResult `json:"shots"`
	VoiceSkippedShotIDs []string             `json:"voiceSkippedShotIds,omitempty"`
	ConcatJobID         string               `json:"concatJobId,omitempty"`
	FinalResourceID     string               `json:"finalResourceId,omitempty"`
}

type RenderedShotResult struct {
	ShotID              string `json:"shotId"`
	Position            int    `json:"position"`
	JobID               string `json:"jobId"`
	VideoPath           string `json:"videoPath"`
	LastFramePath       string `json:"lastFramePath,omitempty"`
	VideoArtifactID     string `json:"videoArtifactId"`
	LastFrameArtifactID string `json:"lastFrameArtifactId,omitempty"`
}

const renderAllShotTempPrefix = "canvas-shot-ref-"

func (s *Service) RenderAllProjectShots(ctx context.Context, userID string, projectID string, req RenderAllShotsRequest) (*RenderAllShotsResult, error) {
	project, err := s.activeProjectForUser(userID, projectID)
	if err != nil {
		return nil, err
	}
	gateway := s.mediaGateway()
	shots, err := s.repo.ProjectShots(projectID)
	if err != nil {
		return nil, err
	}
	if len(shots) == 0 {
		return nil, BadAuthRequest("项目还没有分镜")
	}
	start := 0
	if fromID := strings.TrimSpace(req.FromShotID); fromID != "" {
		start = -1
		for index := range shots {
			if shots[index].ID == fromID {
				start = index
				break
			}
		}
		if start < 0 {
			return nil, NotFound("续跑起点镜头不存在")
		}
	}
	references, err := s.repo.ProjectShotAssetReferences(projectID)
	if err != nil {
		return nil, err
	}
	referencesByShot := make(map[string][]model.ShotAssetReference, len(shots))
	for _, reference := range references {
		referencesByShot[reference.ShotID] = append(referencesByShot[reference.ShotID], reference)
	}

	result := &RenderAllShotsResult{ProjectID: projectID, Shots: make([]RenderedShotResult, 0, len(shots)-start)}
	previousLastFrame := ""
	var concatPaths []string
	var totalDurationMs int64
	for index := start; index < len(shots); index++ {
		shot := shots[index]
		rendered, lastFramePath, voiceSkipped, renderErr := s.renderProjectShot(ctx, gateway, userID, projectID, &shot, previousLastFrame, referencesByShot[shot.ID], req)
		if renderErr != nil {
			title := strings.TrimSpace(shot.Title)
			if title == "" {
				title = shot.ID
			}
			return nil, fmt.Errorf("镜头「%s」渲染失败：%w", title, renderErr)
		}
		if voiceSkipped {
			result.VoiceSkippedShotIDs = append(result.VoiceSkippedShotIDs, shot.ID)
		}
		if index > start && previousLastFrame == "" {
			// 上一镜没拿到尾帧：静默重建首帧会让多镜连续性无声断裂，宁可显式失败
			title := strings.TrimSpace(shot.Title)
			if title == "" {
				title = shot.ID
			}
			return nil, fmt.Errorf("镜头「%s」未返回尾帧，多镜连续性中断", title)
		}
		result.Shots = append(result.Shots, rendered)
		previousLastFrame = lastFramePath
		concatPaths = append(concatPaths, rendered.VideoPath)
		totalDurationMs += shot.DurationMs
	}

	// 单镜头无需拼接；成片登记为素材库 Resource。
	if len(concatPaths) >= 2 {
		job, err := gateway.createJob(ctx, "concat", map[string]any{"shots": concatPaths, "output_name": "render-all-final.mp4"})
		if err != nil {
			return nil, fmt.Errorf("提交成片拼接失败：%w", err)
		}
		job, err = gateway.wait(ctx, job.ID)
		if err != nil {
			return nil, fmt.Errorf("成片拼接失败：%w", err)
		}
		resource, err := s.registerGatewayFileResource(userID, project.Name, job.OutputPath, totalDurationMs)
		if err != nil {
			result.ConcatJobID = job.ID
			return result, fmt.Errorf("成片已生成（%s）但登记素材库失败：%w", job.OutputPath, err)
		}
		result.ConcatJobID = job.ID
		result.FinalResourceID = resource.ID
	}
	return result, nil
}

// renderProjectShot 渲染单个镜头并回写产物；返回尾帧路径供下一镜头接力。
func (s *Service) renderProjectShot(ctx context.Context, gateway *gatewayClient, userID string, projectID string, shot *model.Shot, previousLastFrame string, references []model.ShotAssetReference, req RenderAllShotsRequest) (RenderedShotResult, string, bool, error) {
	// 渲染可达数分钟：HTTP 客户端断开/代理超时不应中断产物链，
	// 否则 gateway 侧 job 成孤儿继续烧算力，而本地 artifact/状态全部缺失。
	ctx = context.WithoutCancel(ctx)
	result := RenderedShotResult{ShotID: shot.ID, Position: shot.Position}
	if strings.TrimSpace(shot.CurrentRevisionID) == "" {
		return result, "", false, BadAuthRequest("镜头缺少分镜版本")
	}
	revision, err := s.repo.ShotRevisionForShot(shot.ID, shot.CurrentRevisionID)
	if err != nil {
		return result, "", false, err
	}
	continueChain := strings.TrimSpace(previousLastFrame) != ""
	params := map[string]any{}
	if firstNonEmpty(strings.TrimSpace(revision.VideoPrompt), revision.PlotDescription) == "" && !req.Draft {
		return result, "", false, BadAuthRequest("镜头缺少画面提示词")
	}
	if !continueChain && !req.Draft {
		imagePrompt := strings.TrimSpace(revision.ImagePrompt)
		if imagePrompt == "" {
			return result, "", false, BadAuthRequest("镜头缺少画面提示词，无法生成首帧")
		}
		params["image"] = map[string]any{"prompt": imagePrompt}
	}
	seconds := float64(revision.DurationMs) / 1000
	if seconds <= 0 {
		seconds = float64(shot.DurationMs) / 1000
	}
	video := map[string]any{
		"prompt":      firstNonEmpty(strings.TrimSpace(revision.VideoPrompt), revision.PlotDescription),
		"first_frame": "auto",
		// P0 定档：完整链走 quality（D 档，4.6× vs reference）；草稿另有 512x288 快路径
		"profile": "quality",
	}
	if continueChain {
		video["first_frame"] = previousLastFrame
	}
	if seconds > 0 {
		video["seconds"] = seconds
	}
	if req.Draft {
		if firstNonEmpty(strings.TrimSpace(revision.VideoPrompt), revision.PlotDescription) == "" {
			return result, "", false, BadAuthRequest("草稿镜头缺少画面提示词")
		}
		video["profile"] = "draft" // 覆盖上面误设的 quality（6步/internal，512x288 下无 internal）
		// ponytail: 草稿固定 512x288，竖屏分寸需要按项目画幅细分时再查 project
		video["width"], video["height"] = 512, 288
		if !continueChain {
			delete(video, "first_frame") // 草稿没有 image 阶段，纯文生视频
		}
	}
	paths, cleanup, resolveErr := s.resolveShotReferencePaths(userID, references)
	defer cleanup()
	if resolveErr != nil {
		return result, "", false, resolveErr
	}
	if len(paths) > 0 {
		video["refs"] = paths
	}
	params["video"] = video
	voiceSkipped := false
	dialogue := strings.TrimSpace(revision.Dialogue)
	if !req.Draft && dialogue != "" {
		if voiceKey := s.shotVoiceKey(userID, references); voiceKey != "" {
			params["voice"] = map[string]any{"text": dialogue, "voice": voiceKey}
		} else {
			voiceSkipped = true
		}
	}
	if musicPrompt := strings.TrimSpace(req.MusicPrompt); musicPrompt != "" && !req.Draft {
		music := map[string]any{"prompt": musicPrompt}
		if seconds > 0 {
			music["duration_s"] = seconds
		}
		params["music"] = music
	}

	job, err := gateway.createJob(ctx, "shot", params)
	if err != nil {
		return result, "", voiceSkipped, err
	}
	result.JobID = job.ID
	job, err = gateway.wait(ctx, job.ID)
	if err != nil {
		return result, "", voiceSkipped, err
	}
	result.VideoPath = firstNonEmpty(job.OutputPath, gateway.metaString(job, "output_path"))
	if result.VideoPath == "" {
		return result, "", voiceSkipped, fmt.Errorf("gateway job %s 未返回视频路径", job.ID)
	}
	result.LastFramePath = gateway.metaString(job, "last_frame_path")
	now := time.Now()
	metadata, err := json.Marshal(map[string]string{"gatewayJobId": job.ID, "videoPath": result.VideoPath, "lastFramePath": result.LastFramePath})
	if err != nil {
		return result, "", voiceSkipped, err
	}
	videoArtifact := &model.ShotArtifact{ID: newID(), ProjectID: projectID, UnitID: shot.UnitID, ShotID: shot.ID, RevisionID: shot.CurrentRevisionID, Type: "video", Status: "ready", Selected: !req.Draft, MetadataJSON: string(metadata), CreatedAt: now, UpdatedAt: now}
	if req.Draft {
		metadata, _ = json.Marshal(map[string]string{"gatewayJobId": job.ID, "videoPath": result.VideoPath, "lastFramePath": result.LastFramePath, "draft": "true"})
		videoArtifact.MetadataJSON = string(metadata)
	}
	if err := s.repo.CreateShotArtifact(videoArtifact); err != nil {
		return result, "", voiceSkipped, err
	}
	result.VideoArtifactID = videoArtifact.ID
	if result.LastFramePath != "" {
		frameMetadata, _ := json.Marshal(map[string]string{"gatewayJobId": job.ID, "path": result.LastFramePath})
		frameArtifact := &model.ShotArtifact{ID: newID(), ProjectID: projectID, UnitID: shot.UnitID, ShotID: shot.ID, RevisionID: shot.CurrentRevisionID, Type: "shot_last_frame", Status: "ready", Selected: !req.Draft, MetadataJSON: string(frameMetadata), CreatedAt: now, UpdatedAt: now}
		if err := s.repo.CreateShotArtifact(frameArtifact); err != nil {
			return result, "", voiceSkipped, err
		}
		result.LastFrameArtifactID = frameArtifact.ID
	}
	if !req.Draft { // 草稿不改镜头状态、不进正式产物链
		shot.Status = "completed"
		shot.UpdatedAt = now
		if err := s.repo.SaveShot(shot, false); err != nil {
			return result, "", voiceSkipped, err
		}
	}
	return result, result.LastFramePath, voiceSkipped, nil
}

// shotVoiceKey 从镜头引用的资产版本反查角色声音绑定；找不到返回空串（跳过配音）。
func (s *Service) shotVoiceKey(userID string, references []model.ShotAssetReference) string {
	for _, reference := range references {
		binding, err := s.repo.CharacterVoiceBinding(reference.AssetVersionID)
		if err != nil || strings.TrimSpace(binding.VoiceProfileID) == "" {
			continue
		}
		profile, err := s.repo.VoiceProfileForUser(userID, binding.VoiceProfileID)
		if err != nil || strings.TrimSpace(profile.VoiceKey) == "" {
			continue
		}
		return profile.VoiceKey
	}
	return ""
}

// resolveShotReferencePaths 把镜头引用的资产版本解析成本地文件路径供 gateway refs 使用。
// 本地存储直接用路径；OSS 等远端对象下载到临时文件，由返回的 cleanup 统一删除。
func (s *Service) resolveShotReferencePaths(userID string, references []model.ShotAssetReference) ([]string, func(), error) {
	cleanups := make([]func(), 0, len(references))
	cleanup := func() {
		for _, fn := range cleanups {
			fn()
		}
	}
	paths := make([]string, 0, len(references))
	for _, reference := range references {
		representations, err := s.repo.AssetRepresentations(reference.AssetVersionID)
		if err != nil {
			return nil, cleanup, err
		}
		resourceID := referenceResourceID(representations)
		if resourceID == "" {
			continue
		}
		resource, err := s.repo.ResourceForUser(userID, resourceID)
		if err != nil {
			return nil, cleanup, err
		}
		if resource.Provider == "local" {
			path := filepath.Join(s.dataDir, "resources", filepath.FromSlash(resource.ObjectKey))
			if _, statErr := os.Stat(path); statErr == nil {
				paths = append(paths, path)
				continue
			}
		}
		tempPath, tempErr := s.downloadResourceToTemp(userID, resource)
		if tempErr != nil {
			return nil, cleanup, tempErr
		}
		cleanups = append(cleanups, func() { os.Remove(tempPath) })
		paths = append(paths, tempPath)
	}
	return paths, cleanup, nil
}

func referenceResourceID(representations []model.AssetRepresentation) string {
	fallback := ""
	for _, representation := range representations {
		if strings.TrimSpace(representation.ResourceID) == "" {
			continue
		}
		switch representation.Role {
		case "primary", "output":
			return representation.ResourceID
		case "":
			fallback = representation.ResourceID
		}
		if fallback == "" {
			fallback = representation.ResourceID
		}
	}
	return fallback
}

func (s *Service) downloadResourceToTemp(userID string, resource *model.Resource) (string, error) {
	_, body, err := s.OpenResource(userID, resource.ID)
	if err != nil {
		return "", err
	}
	defer body.Close()
	temp, err := os.CreateTemp("", renderAllShotTempPrefix+"*"+filepath.Ext(resource.ObjectKey))
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(temp, body)
	closeErr := temp.Close()
	if copyErr != nil {
		os.Remove(temp.Name())
		return "", copyErr
	}
	if closeErr != nil {
		os.Remove(temp.Name())
		return "", closeErr
	}
	return temp.Name(), nil
}

// registerGatewayFileResource 把 gateway 产出的本地成片文件登记进用户素材库。
func (s *Service) registerGatewayFileResource(userID string, projectTitle string, path string, durationMs int64) (*model.Resource, error) {
	if strings.TrimSpace(path) == "" {
		return nil, BadAuthRequest("成片文件路径为空")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(projectTitle)
	if title == "" {
		title = "render-all"
	}
	resource, _, err := s.storeResource(userID, "video", shortTitle(title, 60)+"-render-all.mp4", "video/mp4", stat.Size(), 0, 0, durationMs, file, nil)
	return resource, err
}

// RenderProjectShot 渲染单个分镜：走 shot worker 完整链（image→voice→video→music→混音，
// 台词 Ref2VA + h3 音轨静音），不做跨镜续链与拼接。供「生成镜头视频」按钮直连，
// 替换裸 canvas_video 快路径（那条路没有 voice/music，且 h3 自带音轨是噪音源）。
func (s *Service) RenderProjectShot(ctx context.Context, userID string, projectID string, shotID string, req RenderAllShotsRequest) (*RenderedShotResult, error) {
	if _, err := s.activeProjectForUser(userID, projectID); err != nil {
		return nil, err
	}
	gateway := s.mediaGateway()
	shots, err := s.repo.ProjectShots(projectID)
	if err != nil {
		return nil, err
	}
	var shot *model.Shot
	for index := range shots {
		if shots[index].ID == shotID {
			shot = &shots[index]
			break
		}
	}
	if shot == nil {
		return nil, NotFound("分镜不存在")
	}
	references, err := s.repo.ProjectShotAssetReferences(projectID)
	if err != nil {
		return nil, err
	}
	var shotRefs []model.ShotAssetReference
	for _, reference := range references {
		if reference.ShotID == shotID {
			shotRefs = append(shotRefs, reference)
		}
	}
	rendered, _, _, err := s.renderProjectShot(ctx, gateway, userID, projectID, shot, "", shotRefs, req)
	if err != nil {
		return nil, err
	}
	return &rendered, nil
}
