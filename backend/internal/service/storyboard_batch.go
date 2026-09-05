package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"infinite-canvas/backend/internal/model"
)

// 分镜批量编排三件套：一键视频（fan-out 行任务）、一键音乐（按段生成）、合成（concat）。
// 行视频子任务的编排复用 storyboard_row_video；父任务只负责入队、轮询影策任务表、
// 汇总结果。注意：父任务在 worker 里占一个并发槽轮询子任务，子任务需要其他空闲槽领取。

const (
	storyboardVideoBatchTaskType = "storyboard_video_batch"
	storyboardMusicBatchTaskType = "storyboard_music_batch"
	storyboardComposeTaskType    = "storyboard_compose"

	// 音乐段的固定后缀：配乐只做纯音乐底，不抢对白。
	storyboardMusicPromptSuffix = "纯音乐，无人声，电影配乐质感"
	// 缺省音乐分组：存量行没有 musicGroupId 时全部落进同一段。
	storyboardDefaultMusicGroupID = "seg-01"

	storyboardBatchPollInterval = 2 * time.Second
)

// --- 请求与落库输入 ---

type StoryboardVideoBatchRequest struct {
	ProjectID             string                `json:"projectId"`
	Rows                  []agentStoryboardShot `json:"rows"`
	FirstFrameResourceIDs map[int]string        `json:"firstFrameResourceIds"`
	Width                 int                   `json:"width"`
	Height                int                   `json:"height"`
}

type StoryboardMusicBatchRequest struct {
	ProjectID string                `json:"projectId"`
	Rows      []agentStoryboardShot `json:"rows"`
}

type StoryboardComposeRequest struct {
	ProjectID        string                `json:"projectId"`
	Rows             []agentStoryboardShot `json:"rows"`
	VideoResourceIDs map[int]string        `json:"videoResourceIds"`
	MusicResourceIDs map[string]string     `json:"musicResourceIds"`
	BgmGainDB        float64               `json:"bgmGainDb"`
}

type storyboardVideoBatchInput struct {
	ProjectID             string                `json:"projectId"`
	Rows                  []agentStoryboardShot `json:"rows"`
	FirstFrameResourceIDs map[int]string        `json:"firstFrameResourceIds"`
	Width                 int                   `json:"width"`
	Height                int                   `json:"height"`
}

type storyboardMusicBatchInput struct {
	ProjectID string                `json:"projectId"`
	Rows      []agentStoryboardShot `json:"rows"`
}

type storyboardComposeInput struct {
	ProjectID        string                `json:"projectId"`
	Rows             []agentStoryboardShot `json:"rows"`
	VideoResourceIDs map[int]string        `json:"videoResourceIds"`
	MusicResourceIDs map[string]string     `json:"musicResourceIds"`
	BgmGainDB        float64               `json:"bgmGainDb"`
}

// --- 任务创建 ---

// CreateStoryboardVideoBatchTask 一键视频：每行入队一个 storyboard_row_video 子任务。
func (s *Service) CreateStoryboardVideoBatchTask(userID string, req StoryboardVideoBatchRequest) (*model.Task, error) {
	if strings.TrimSpace(req.ProjectID) == "" {
		return nil, BadAuthRequest("projectId is required")
	}
	if len(req.Rows) == 0 {
		return nil, BadAuthRequest("rows is required")
	}
	if (req.Width > 0) != (req.Height > 0) {
		return nil, BadAuthRequest("width 与 height 必须同时提供")
	}
	input := storyboardVideoBatchInput{
		ProjectID: strings.TrimSpace(req.ProjectID), Rows: req.Rows,
		FirstFrameResourceIDs: req.FirstFrameResourceIDs, Width: req.Width, Height: req.Height,
	}
	return s.createStoryboardBatchTask(userID, input.ProjectID, storyboardVideoBatchTaskType, fmt.Sprintf("一键视频：%d 个镜头", len(req.Rows)), input)
}

// CreateStoryboardMusicBatchTask 一键音乐：按 musicGroupId 分段生成配乐。
func (s *Service) CreateStoryboardMusicBatchTask(userID string, req StoryboardMusicBatchRequest) (*model.Task, error) {
	if strings.TrimSpace(req.ProjectID) == "" {
		return nil, BadAuthRequest("projectId is required")
	}
	if len(req.Rows) == 0 {
		return nil, BadAuthRequest("rows is required")
	}
	input := storyboardMusicBatchInput{ProjectID: strings.TrimSpace(req.ProjectID), Rows: req.Rows}
	return s.createStoryboardBatchTask(userID, input.ProjectID, storyboardMusicBatchTaskType, fmt.Sprintf("一键音乐：%d 个镜头", len(req.Rows)), input)
}

// CreateStoryboardComposeTask 合成：行视频按镜头序拼接，音乐段按起始顺序对齐。
func (s *Service) CreateStoryboardComposeTask(userID string, req StoryboardComposeRequest) (*model.Task, error) {
	if strings.TrimSpace(req.ProjectID) == "" {
		return nil, BadAuthRequest("projectId is required")
	}
	if len(req.Rows) < 2 {
		return nil, BadAuthRequest("合成至少需要两个镜头行")
	}
	input := storyboardComposeInput{
		ProjectID: strings.TrimSpace(req.ProjectID), Rows: req.Rows,
		VideoResourceIDs: req.VideoResourceIDs, MusicResourceIDs: req.MusicResourceIDs, BgmGainDB: req.BgmGainDB,
	}
	return s.createStoryboardBatchTask(userID, input.ProjectID, storyboardComposeTaskType, fmt.Sprintf("分镜合成：%d 个镜头", len(req.Rows)), input)
}

// createStoryboardBatchTask 三个批量任务共用的入队序曲，与 CreateStoryboardRowVideoTask 同一套配额/校验。
func (s *Service) createStoryboardBatchTask(userID string, projectID string, taskType string, prompt string, input any) (*model.Task, error) {
	if s.IsDraining() {
		return nil, &AppError{Status: 503, Code: 503, Message: "服务正在维护，暂不接受新的生成任务", Retryable: true}
	}
	policy, err := s.RuntimePolicy()
	if err != nil {
		return nil, err
	}
	activeTasks, err := s.repo.ActiveTaskCountForUser(userID)
	if err != nil {
		return nil, err
	}
	if activeTasks >= int64(policy.Task.ActiveTaskLimit) {
		return nil, BadAuthRequest(fmt.Sprintf("同时排队或运行的任务最多 %d 个，请等待已有任务完成", policy.Task.ActiveTaskLimit))
	}
	task := model.Task{
		ID: newID(), UserID: userID, ProjectID: projectID,
		Type: taskType, Status: model.TaskStatusQueued, Stage: "等待队列调度", Progress: 5,
		Operation: taskType, Prompt: truncateRunes(prompt, 200),
	}
	if err := s.protectTaskSecrets(input); err != nil {
		return nil, err
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("序列化任务输入失败：%w", err)
	}
	task.InputJSON = string(inputJSON)
	if err := s.ensureTaskProjectActive(userID, projectID); err != nil {
		return nil, err
	}
	if err := s.createTaskWithinStorageQuota(&task, nil, policy); err != nil {
		return nil, err
	}
	_ = s.log(userID, task.ID, "info", "分镜批量任务已进入队列", "")
	return taskForOutput(task), nil
}

// --- 执行分发入口 ---

func (s *Service) processStoryboardVideoBatchTask(ctx context.Context, task model.Task) (map[string]interface{}, []map[string]interface{}, error) {
	var input storyboardVideoBatchInput
	if err := json.Unmarshal([]byte(task.InputJSON), &input); err != nil {
		return nil, nil, fmt.Errorf("一键视频任务输入解析失败：%w", err)
	}
	results, err := runStoryboardVideoBatch(ctx, serviceBatchStore{s: s, userID: task.UserID, taskID: task.ID}, input)
	if err != nil {
		return nil, nil, err
	}
	succeeded, failed := countStoryboardBatchRows(results)
	return map[string]interface{}{"rows": results, "succeeded": succeeded, "failed": failed}, nil, nil
}

func (s *Service) processStoryboardMusicBatchTask(ctx context.Context, task model.Task) (map[string]interface{}, []map[string]interface{}, error) {
	var input storyboardMusicBatchInput
	if err := json.Unmarshal([]byte(task.InputJSON), &input); err != nil {
		return nil, nil, fmt.Errorf("一键音乐任务输入解析失败：%w", err)
	}
	project, err := s.activeProjectForUser(task.UserID, input.ProjectID)
	if err != nil {
		return nil, nil, err
	}
	tracks, err := runStoryboardMusicBatch(ctx, s.mediaGateway(), input.Rows, project.Description)
	if err != nil {
		return nil, nil, err
	}
	segments := make([]map[string]interface{}, 0, len(tracks))
	for _, track := range tracks {
		resource, err := s.registerGatewayAudioResource(task.UserID, project.Name, track.WavPath, int64(track.DurationSeconds)*1000)
		if err != nil {
			return nil, nil, fmt.Errorf("登记音乐资源失败：%w", err)
		}
		segments = append(segments, map[string]interface{}{
			"musicGroupId":    track.MusicGroupID,
			"resourceId":      resource.ID,
			"resourceUrl":     "/api/resources/" + resource.ID + "/file",
			"durationSeconds": track.DurationSeconds,
		})
	}
	return map[string]interface{}{"segments": segments}, nil, nil
}

func (s *Service) processStoryboardComposeTask(ctx context.Context, task model.Task) (map[string]interface{}, []map[string]interface{}, error) {
	var input storyboardComposeInput
	if err := json.Unmarshal([]byte(task.InputJSON), &input); err != nil {
		return nil, nil, fmt.Errorf("合成任务输入解析失败：%w", err)
	}
	project, err := s.activeProjectForUser(task.UserID, input.ProjectID)
	if err != nil {
		return nil, nil, err
	}
	plans := planStoryboardMusicGroups(input.Rows, project.Description)
	missingVideos := storyboardComposeMissingVideos(input.Rows, input.VideoResourceIDs)
	missingGroups := storyboardComposeMissingGroups(plans, input.MusicResourceIDs)
	if len(missingVideos) > 0 || len(missingGroups) > 0 {
		return nil, nil, fmt.Errorf("缺少素材无法合成：镜头 %v 缺少视频资源，音乐段 %v 缺少音乐资源", missingVideos, missingGroups)
	}
	shots := make([]composeShotInput, 0, len(input.Rows))
	var totalDurationMs int64
	for index := range input.Rows {
		shotNumber := index + 1
		path, err := s.localResourceFilePath(task.UserID, input.VideoResourceIDs[shotNumber])
		if err != nil {
			return nil, nil, fmt.Errorf("读取镜头 %d 视频资源失败：%w", shotNumber, err)
		}
		shots = append(shots, composeShotInput{ShotNumber: shotNumber, Path: path})
		totalDurationMs += int64(input.Rows[index].Duration) * 1000
	}
	segments := make([]concatMusicSegment, 0, len(plans))
	for _, plan := range plans {
		path, err := s.localResourceFilePath(task.UserID, input.MusicResourceIDs[plan.MusicGroupID])
		if err != nil {
			return nil, nil, fmt.Errorf("读取音乐段 %s 资源失败：%w", plan.MusicGroupID, err)
		}
		segments = append(segments, concatMusicSegment{Path: path, DurationS: float64(plan.DurationSeconds)})
	}
	outputPath, err := runStoryboardCompose(ctx, s.mediaGateway(), shots, segments, input.BgmGainDB)
	if err != nil {
		return nil, nil, err
	}
	resource, err := s.registerGatewayFileResource(task.UserID, project.Name, outputPath, totalDurationMs)
	if err != nil {
		return nil, nil, fmt.Errorf("登记合成成片资源失败：%w", err)
	}
	return map[string]interface{}{
		"videoResourceId": resource.ID,
		"videoResourceUrl": "/api/resources/" + resource.ID + "/file",
	}, nil, nil
}

// --- 一键视频：fan-out + 轮询子任务 ---

// batchRowVideoStore 收敛父任务对子任务队列的全部依赖；单元测试用 fake 模拟 worker 领取。
type batchRowVideoStore interface {
	CreateRowVideoTask(req StoryboardRowVideoTaskRequest) (*model.Task, error)
	ChildTask(id string) (*model.Task, error)
	UpdateBatchProgress(stage string, progress int) error
}

type serviceBatchStore struct {
	s      *Service
	userID string
	taskID string
}

func (st serviceBatchStore) CreateRowVideoTask(req StoryboardRowVideoTaskRequest) (*model.Task, error) {
	return st.s.CreateStoryboardRowVideoTask(st.userID, req)
}

func (st serviceBatchStore) ChildTask(id string) (*model.Task, error) {
	return st.s.repo.Task(id)
}

func (st serviceBatchStore) UpdateBatchProgress(stage string, progress int) error {
	return st.s.repo.UpdateTaskProgress(st.taskID, stage, progress)
}

type storyboardBatchRowResult struct {
	ShotNumber      int    `json:"shotNumber"`
	TaskID          string `json:"taskId,omitempty"`
	Status          string `json:"status"`
	Error           string `json:"error,omitempty"`
	VideoResourceID string `json:"videoResourceId,omitempty"`
}

// runStoryboardVideoBatch 逐行入队子任务，行失败不阻断；父任务进度 = 完成行数/总数。
// 全部行失败时父任务以错误收场；只要 ≥1 行成功父任务即成功。
func runStoryboardVideoBatch(ctx context.Context, store batchRowVideoStore, input storyboardVideoBatchInput) ([]storyboardBatchRowResult, error) {
	rows := input.Rows
	if len(rows) == 0 {
		return nil, BadAuthRequest("rows is required")
	}
	results := make([]storyboardBatchRowResult, len(rows))
	type childRef struct {
		index  int
		taskID string
	}
	var children []childRef
	for index := range rows {
		shotNumber := index + 1
		child, err := store.CreateRowVideoTask(StoryboardRowVideoTaskRequest{
			ProjectID:            input.ProjectID,
			Row:                  batchShotToRowVideoRow(rows[index]),
			FirstFrameResourceID: input.FirstFrameResourceIDs[shotNumber],
			Width:                input.Width,
			Height:               input.Height,
		})
		if err != nil {
			results[index] = storyboardBatchRowResult{ShotNumber: shotNumber, Status: string(model.TaskStatusFailed), Error: err.Error()}
		} else {
			results[index] = storyboardBatchRowResult{ShotNumber: shotNumber, TaskID: child.ID, Status: string(child.Status)}
			children = append(children, childRef{index: index, taskID: child.ID})
		}
		updateStoryboardBatchProgress(store, results)
	}
	for len(children) > 0 {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		var pending []childRef
		for _, child := range children {
			latest, err := store.ChildTask(child.taskID)
			if err != nil {
				results[child.index].Status = string(model.TaskStatusFailed)
				results[child.index].Error = fmt.Sprintf("查询子任务状态失败：%v", err)
				continue
			}
			results[child.index].Status = string(latest.Status)
			if !storyboardTaskTerminal(latest.Status) {
				pending = append(pending, child)
				continue
			}
			if latest.Status == model.TaskStatusSucceeded {
				results[child.index].VideoResourceID = childVideoResourceID(latest.ResultJSON)
			} else {
				results[child.index].Error = firstNonEmpty(strings.TrimSpace(latest.Error), "子任务生成失败")
			}
		}
		children = pending
		if len(children) > 0 {
			updateStoryboardBatchProgress(store, results)
			select {
			case <-ctx.Done():
				return results, ctx.Err()
			case <-time.After(storyboardBatchPollInterval):
			}
		}
	}
	updateStoryboardBatchProgress(store, results)
	if countStoryboardBatchSucceeded(results) == 0 {
		return results, fmt.Errorf("全部 %d 行生成失败，例如：%s", len(results), firstStoryboardRowError(results))
	}
	return results, nil
}

func updateStoryboardBatchProgress(store batchRowVideoStore, results []storyboardBatchRowResult) {
	completed := 0
	for _, result := range results {
		if storyboardTaskTerminal(model.TaskStatus(result.Status)) {
			completed++
		}
	}
	progress := 5 + 90*completed/len(results)
	_ = store.UpdateBatchProgress(fmt.Sprintf("已完成 %d/%d 行", completed, len(results)), progress)
}

func storyboardTaskTerminal(status model.TaskStatus) bool {
	return status == model.TaskStatusSucceeded || status == model.TaskStatusFailed || status == model.TaskStatusCancelled
}

func countStoryboardBatchSucceeded(results []storyboardBatchRowResult) int {
	count := 0
	for _, result := range results {
		if result.Status == string(model.TaskStatusSucceeded) {
			count++
		}
	}
	return count
}

func countStoryboardBatchRows(results []storyboardBatchRowResult) (int, int) {
	succeeded := countStoryboardBatchSucceeded(results)
	return succeeded, len(results) - succeeded
}

func firstStoryboardRowError(results []storyboardBatchRowResult) string {
	for _, result := range results {
		if result.Error != "" {
			return result.Error
		}
	}
	return ""
}

func childVideoResourceID(resultJSON string) string {
	var parsed struct {
		VideoResourceID string `json:"videoResourceId"`
	}
	if json.Unmarshal([]byte(resultJSON), &parsed) != nil {
		return ""
	}
	return parsed.VideoResourceID
}

// batchShotToRowVideoRow 把分镜行落库形态转成 row_video 编排所需子集。
// CharacterIDs 是角色资产 ID，不携带音色版本；批量路径使用渠道默认音色。
func batchShotToRowVideoRow(shot agentStoryboardShot) *storyboardRowVideoRow {
	characters := make([]storyboardRowCharacterRef, 0, len(shot.CharacterIDs))
	for _, assetID := range shot.CharacterIDs {
		characters = append(characters, storyboardRowCharacterRef{CharacterAssetID: assetID})
	}
	return &storyboardRowVideoRow{
		Title:             shot.Title,
		Dialogue:          shot.Dialogue,
		DurationSeconds:   shot.Duration,
		Characters:        characters,
		// 视频提示词优先级：videoMotionPrompt（完整分段）> videoPrompt > motion（仅运镜）
		VideoMotionPrompt: firstNonEmpty(strings.TrimSpace(shot.VideoMotionPrompt),
			strings.TrimSpace(shot.VideoPrompt), strings.TrimSpace(shot.Motion)),
		VideoPrompt:       shot.VideoPrompt,
		AudioEffects:      shot.AudioEffects,
		VoiceMode:         shot.VoiceMode,
		SfxTags:           shot.SfxTags,
	}
}

// --- 一键音乐：按 musicGroupId 分段生成 ---

type storyboardMusicGroupPlan struct {
	MusicGroupID    string
	Prompt          string
	DurationSeconds int
}

type storyboardMusicTrackResult struct {
	MusicGroupID    string
	WavPath         string
	DurationSeconds int
}

type storyboardMusicWav struct {
	WavPath         string
	DurationSeconds float64
}

// musicGateway 是一键音乐对 Gateway 的全部依赖；单元测试用 httptest stub。
type musicGateway interface {
	CreateMusicTrack(ctx context.Context, prompt string, durationSeconds int) (storyboardMusicWav, error)
}

// planStoryboardMusicGroups 按 musicGroupId 分组（空→seg-01），组内时长求和并 clamp 到 [10,600]；
// prompt = 组内首个 musicMood（空则用项目风格描述）+ 固定后缀；分组按首次出现顺序排列。
func planStoryboardMusicGroups(rows []agentStoryboardShot, styleDescription string) []storyboardMusicGroupPlan {
	order := make([]string, 0, len(rows))
	groups := make(map[string]*storyboardMusicGroupPlan, len(rows))
	for _, shot := range rows {
		groupID := strings.TrimSpace(shot.MusicGroupID)
		if groupID == "" {
			groupID = storyboardDefaultMusicGroupID
		}
		plan, ok := groups[groupID]
		if !ok {
			prompt := storyboardMusicPromptSuffix
			if base := firstNonEmpty(strings.TrimSpace(shot.MusicMood), strings.TrimSpace(styleDescription)); base != "" {
				prompt = base + "，" + storyboardMusicPromptSuffix
			}
			plan = &storyboardMusicGroupPlan{MusicGroupID: groupID, Prompt: prompt}
			groups[groupID] = plan
			order = append(order, groupID)
		}
		plan.DurationSeconds += shot.Duration
	}
	plans := make([]storyboardMusicGroupPlan, 0, len(order))
	for _, groupID := range order {
		plan := *groups[groupID]
		if plan.DurationSeconds < 10 {
			plan.DurationSeconds = 10
		}
		if plan.DurationSeconds > 600 {
			plan.DurationSeconds = 600
		}
		plans = append(plans, plan)
	}
	return plans
}

// runStoryboardMusicBatch 组间串行调用 /v1/music；任一段失败即整体失败。
func runStoryboardMusicBatch(ctx context.Context, gw musicGateway, rows []agentStoryboardShot, styleDescription string) ([]storyboardMusicTrackResult, error) {
	plans := planStoryboardMusicGroups(rows, styleDescription)
	results := make([]storyboardMusicTrackResult, 0, len(plans))
	for _, plan := range plans {
		wav, err := gw.CreateMusicTrack(ctx, plan.Prompt, plan.DurationSeconds)
		if err != nil {
			return results, fmt.Errorf("音乐段 %s 生成失败：%w", plan.MusicGroupID, err)
		}
		if strings.TrimSpace(wav.WavPath) == "" {
			return results, fmt.Errorf("音乐段 %s 未返回音频文件", plan.MusicGroupID)
		}
		results = append(results, storyboardMusicTrackResult{MusicGroupID: plan.MusicGroupID, WavPath: wav.WavPath, DurationSeconds: plan.DurationSeconds})
	}
	return results, nil
}

// --- 合成：concat 行视频 + 音乐段 ---

type composeShotInput struct {
	ShotNumber int
	Path       string
}

type concatMusicSegment struct {
	Path      string  `json:"path"`
	DurationS float64 `json:"duration_s"`
}

type concatJobRequest struct {
	Shots         []string             `json:"shots"`
	MusicSegments []concatMusicSegment `json:"music_segments,omitempty"`
	BgmGainDB     float64              `json:"bgm_gain_db"`
}

// composeGateway 是合成对 Gateway 的全部依赖。
type composeGateway interface {
	CreateConcatJob(ctx context.Context, req concatJobRequest) (string, error)
	WaitConcatOutput(ctx context.Context, jobID string) (string, error)
}

// runStoryboardCompose 提交 /v1/concat 并轮询到完成，返回成片本地路径。
func runStoryboardCompose(ctx context.Context, gw composeGateway, shots []composeShotInput, segments []concatMusicSegment, bgmGainDB float64) (string, error) {
	if len(shots) < 2 {
		return "", BadAuthRequest("合成至少需要两段镜头视频")
	}
	sorted := append([]composeShotInput(nil), shots...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ShotNumber < sorted[j].ShotNumber })
	paths := make([]string, 0, len(sorted))
	for _, shot := range sorted {
		paths = append(paths, shot.Path)
	}
	jobID, err := gw.CreateConcatJob(ctx, concatJobRequest{Shots: paths, MusicSegments: segments, BgmGainDB: bgmGainDB})
	if err != nil {
		return "", fmt.Errorf("提交合成任务失败：%w", err)
	}
	outputPath, err := gw.WaitConcatOutput(ctx, jobID)
	if err != nil {
		return "", fmt.Errorf("合成失败（job %s）：%w", jobID, err)
	}
	if strings.TrimSpace(outputPath) == "" {
		return "", fmt.Errorf("合成 job %s 未返回输出文件", jobID)
	}
	return outputPath, nil
}

func storyboardComposeMissingVideos(rows []agentStoryboardShot, videoResourceIDs map[int]string) []int {
	missing := []int{}
	for index := range rows {
		if strings.TrimSpace(videoResourceIDs[index+1]) == "" {
			missing = append(missing, index+1)
		}
	}
	return missing
}

func storyboardComposeMissingGroups(plans []storyboardMusicGroupPlan, musicResourceIDs map[string]string) []string {
	missing := []string{}
	for _, plan := range plans {
		if strings.TrimSpace(musicResourceIDs[plan.MusicGroupID]) == "" {
			missing = append(missing, plan.MusicGroupID)
		}
	}
	return missing
}

// localResourceFilePath 本地存储资源的绝对路径；gateway 产物同机落库，合成直接读文件。
func (s *Service) localResourceFilePath(userID string, resourceID string) (string, error) {
	resource, err := s.repo.ResourceForUser(userID, strings.TrimSpace(resourceID))
	if err != nil {
		return "", err
	}
	if resource.Status != model.ResourceStatusReady {
		return "", BadAuthRequest("资源尚未就绪：" + resourceID)
	}
	if resource.Provider != "local" {
		return "", BadAuthRequest("资源不在本地存储，无法直接读取：" + resourceID)
	}
	// 绝对路径：相对路径只在本进程 cwd 下有效，传给 Gateway（不同 cwd）就读不到了
	abs, err := filepath.Abs(filepath.Join(s.dataDir, "resources", filepath.FromSlash(resource.ObjectKey)))
	if err != nil {
		return "", err
	}
	return abs, nil
}

// --- gatewayClient 对 musicGateway/composeGateway 的实现 ---

func (c *gatewayClient) CreateMusicTrack(ctx context.Context, prompt string, durationSeconds int) (storyboardMusicWav, error) {
	var payload struct {
		URL             string  `json:"url"`
		DurationSeconds float64 `json:"duration_seconds"`
	}
	if err := c.getGatewayJSON(ctx, http.MethodPost, "/v1/music", map[string]any{"prompt": prompt, "duration_s": durationSeconds}, &payload); err != nil {
		return storyboardMusicWav{}, err
	}
	if strings.TrimSpace(payload.URL) == "" {
		return storyboardMusicWav{}, fmt.Errorf("gateway /v1/music 未返回音频地址")
	}
	wavPath, err := c.downloadGatewayFile(ctx, payload.URL, ".wav")
	if err != nil {
		return storyboardMusicWav{}, fmt.Errorf("下载音乐失败：%w", err)
	}
	return storyboardMusicWav{WavPath: wavPath, DurationSeconds: payload.DurationSeconds}, nil
}

func (c *gatewayClient) CreateConcatJob(ctx context.Context, req concatJobRequest) (string, error) {
	return c.postGatewayForID(ctx, "/v1/concat", req)
}

// WaitConcatOutput 轮询 GET /v1/videos/{id} 到 completed，再下载 /content 成本地 mp4。
func (c *gatewayClient) WaitConcatOutput(ctx context.Context, jobID string) (string, error) {
	deadline := time.Now().Add(c.jobTimeout)
	for {
		var videoTask struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		if err := c.getGatewayJSON(ctx, http.MethodGet, "/v1/videos/"+jobID, nil, &videoTask); err != nil {
			return "", err
		}
		switch videoTask.Status {
		case "completed":
			return c.downloadGatewayFile(ctx, c.baseURL+"/v1/videos/"+jobID+"/content", ".mp4")
		case "failed", "cancelled":
			return "", fmt.Errorf("gateway video task %s %s: %s", jobID, videoTask.Status, firstNonEmpty(strings.TrimSpace(videoTask.Error), "无错误信息"))
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("gateway video task %s 等待超时（>%s）", jobID, c.jobTimeout)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(c.pollInterval):
		}
	}
}

// registerGatewayAudioResource 把 gateway 产出的音乐 wav 登记进用户素材库。
func (s *Service) registerGatewayAudioResource(userID string, projectTitle string, path string, durationMs int64) (*model.Resource, error) {
	if strings.TrimSpace(path) == "" {
		return nil, BadAuthRequest("音频文件路径为空")
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
		title = "storyboard"
	}
	resource, _, err := s.storeResource(userID, "audio", shortTitle(title, 60)+"-music.wav", "audio/wav", stat.Size(), 0, 0, durationMs, file, nil)
	return resource, err
}
