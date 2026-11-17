package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"infinite-canvas/backend/internal/model"
)

// 单个分镜行的视频生成编排：/v1/videos → /v1/mix（sfx）→ 资源库。
// 配音链（/v1/audio/speech）已随 MediaGateway TTS 引擎删除而移除，行视频为无声视频。
// Gateway 面见 MediaGateway 的 /v1/videos、/v1/mix；任务共用同一个 job 存储，
// 轮询走 /v1/jobs/{id}（gatewayClient.wait），completed 时直接读 output_path。
// 失败即标 failed 上抛，不重试（重试策略由批量层做）。

const storyboardRowVideoTaskType = "storyboard_row_video"

// StoryboardRowVideoTaskRequest 创建单个分镜行视频任务；批量按钮下一期按行循环调用。
type StoryboardRowVideoTaskRequest struct {
	ProjectID            string                 `json:"projectId"`
	Row                  *storyboardRowVideoRow `json:"row"`
	FirstFrameResourceID string                 `json:"firstFrameResourceId"`
	Width                int                    `json:"width"`
	Height               int                    `json:"height"`
}

type storyboardRowVideoInput struct {
	Row                  storyboardRowVideoRow `json:"row"`
	ProjectID            string                `json:"projectId"`
	FirstFrameResourceID string                `json:"firstFrameResourceId"`
	Width                int                   `json:"width"`
	Height               int                   `json:"height"`
}

// storyboardRowVideoRow 是画布分镜行（agentStoryboardShot 落库形态）的编排所需子集。
type storyboardRowVideoRow struct {
	Title             string                      `json:"title"`
	Dialogue          string                      `json:"dialogue"`
	DurationSeconds   int                         `json:"durationSeconds"`
	Characters        []storyboardRowCharacterRef `json:"characters"`
	VideoMotionPrompt string                      `json:"videoMotionPrompt"`
	VideoPrompt       string                      `json:"videoPrompt"`
	AudioEffects      string                      `json:"audioEffects"`
	VoiceMode         string                      `json:"voiceMode"`
	SfxTags           []string                    `json:"sfxTags"`
}

type storyboardRowCharacterRef struct {
	CharacterName      string `json:"characterName"`
	CharacterAssetID   string `json:"characterAssetId"`
	CharacterVersionID string `json:"characterVersionId"`
}

// rowVideoGateway 是编排对 Gateway 的全部依赖；单元测试用接口 stub，不打真网关。
type rowVideoGateway interface {
	CreateVideoJob(ctx context.Context, req rowVideoJobRequest) (string, error)
	CreateMixJob(ctx context.Context, videoJobID string, tracks []rowVideoMixTrack, keepSourceAudio bool) (string, error)
	WaitJob(ctx context.Context, jobID string) (rowVideoJobResult, error)
}

type rowVideoJobRequest struct {
	Prompt          string   `json:"prompt"`
	Size            string   `json:"size,omitempty"`
	Seconds         int      `json:"seconds,omitempty"`
	ReferenceImages []string `json:"reference_images,omitempty"`
}

type rowVideoMixTrack struct {
	SfxTag string  `json:"sfx_tag,omitempty"`
	Path   string  `json:"path,omitempty"`
	GainDB float64 `json:"gain_db"`
	StartS float64 `json:"start_s"`
}

type rowVideoJobResult struct {
	OutputPath string
}

type rowVideoParams struct {
	Row               storyboardRowVideoRow
	FirstFrameDataURL string
	Width             int
	Height            int
}

type rowVideoOutcome struct {
	VideoJobID    string
	MixJobID      string
	MixOutputPath string
	Seconds       int
	VoiceMode     string
	SfxTags       []string
}

// CreateStoryboardRowVideoTask 为单个分镜行入队「视频+声音」编排任务。
func (s *Service) CreateStoryboardRowVideoTask(userID string, req StoryboardRowVideoTaskRequest) (*model.Task, error) {
	if s.IsDraining() {
		return nil, &AppError{Status: 503, Code: 503, Message: "服务正在维护，暂不接受新的生成任务", Retryable: true}
	}
	if strings.TrimSpace(req.ProjectID) == "" {
		return nil, BadAuthRequest("projectId is required")
	}
	if req.Row == nil {
		return nil, BadAuthRequest("row is required")
	}
	if req.Row.DurationSeconds <= 0 || req.Row.DurationSeconds > 60 {
		return nil, BadAuthRequest("分镜行 durationSeconds 必须在 1 到 60 之间")
	}
	if (req.Width > 0) != (req.Height > 0) {
		return nil, BadAuthRequest("width 与 height 必须同时提供")
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
	prompt := rowVideoTaskPrompt(*req.Row)
	task := model.Task{
		ID: newID(), UserID: userID, ProjectID: strings.TrimSpace(req.ProjectID),
		Type: storyboardRowVideoTaskType, Status: model.TaskStatusQueued, Stage: "等待队列调度", Progress: 5,
		Operation: storyboardRowVideoTaskType, Prompt: prompt,
	}
	input := storyboardRowVideoInput{Row: *req.Row, ProjectID: strings.TrimSpace(req.ProjectID), FirstFrameResourceID: strings.TrimSpace(req.FirstFrameResourceID), Width: req.Width, Height: req.Height}
	if err := s.protectTaskSecrets(input); err != nil {
		return nil, err
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("序列化任务输入失败：%w", err)
	}
	task.InputJSON = string(inputJSON)
	if err := s.ensureTaskProjectActive(userID, input.ProjectID); err != nil {
		return nil, err
	}
	if err := s.createTaskWithinStorageQuota(&task, nil, policy); err != nil {
		return nil, err
	}
	_ = s.log(userID, task.ID, "info", "分镜行视频任务已进入队列", "")
	return taskForOutput(task), nil
}

func rowVideoTaskPrompt(row storyboardRowVideoRow) string {
	prompt := firstNonEmpty(strings.TrimSpace(row.VideoMotionPrompt), strings.TrimSpace(row.VideoPrompt))
	if prompt == "" {
		prompt = strings.TrimSpace(row.Title)
	}
	if prompt == "" {
		prompt = strings.TrimSpace(row.Dialogue)
	}
	return truncateRunes(prompt, 200)
}

func (s *Service) processStoryboardRowVideoTask(ctx context.Context, task model.Task) (map[string]interface{}, []map[string]interface{}, error) {
	var input storyboardRowVideoInput
	if err := json.Unmarshal([]byte(task.InputJSON), &input); err != nil {
		return nil, nil, fmt.Errorf("分镜行视频任务输入解析失败：%w", err)
	}
	project, err := s.activeProjectForUser(task.UserID, input.ProjectID)
	if err != nil {
		return nil, nil, err
	}
	firstFrameDataURL := ""
	if input.FirstFrameResourceID != "" {
		firstFrameDataURL, err = s.resourceImageDataURL(task.UserID, input.FirstFrameResourceID)
		if err != nil {
			return nil, nil, fmt.Errorf("读取首帧资源失败：%w", err)
		}
	}
	outcome, err := runStoryboardRowVideo(ctx, s.mediaGateway(), rowVideoParams{
		Row:               input.Row,
		FirstFrameDataURL: firstFrameDataURL,
		Width:             input.Width,
		Height:            input.Height,
	})
	if err != nil {
		return nil, nil, err
	}
	resource, err := s.registerGatewayFileResource(task.UserID, project.Name, outcome.MixOutputPath, int64(outcome.Seconds)*1000)
	if err != nil {
		return nil, nil, fmt.Errorf("登记行视频资源失败：%w", err)
	}
	return map[string]interface{}{
		"videoResourceId":  resource.ID,
		"videoResourceUrl": "/api/resources/" + resource.ID + "/file",
		"videoJobId":       outcome.VideoJobID,
		"mixJobId":         outcome.MixJobID,
		"seconds":          outcome.Seconds,
		"voiceMode":        outcome.VoiceMode,
		"sfxTags":          outcome.SfxTags,
	}, nil, nil
}

// waitStoryboardRowJob 轮询网关 job，容忍网关短暂重启的传输错误
// （kickstart 窗口的 connection refused 不该判死一次数分钟的渲染）。
// 上游明确报 failed/超时则原样返回，快速失败。
func waitStoryboardRowJob(ctx context.Context, gw rowVideoGateway, jobID string) (rowVideoJobResult, error) {
	const attempts = 6
	var lastErr error
	for i := 0; i < attempts; i++ {
		result, err := gw.WaitJob(ctx, jobID)
		if err == nil {
			return result, nil
		}
		var urlErr *url.Error
		if !errors.As(err, &urlErr) || ctx.Err() != nil {
			return rowVideoJobResult{}, err
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return rowVideoJobResult{}, ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return rowVideoJobResult{}, lastErr
}

// runStoryboardRowVideo 执行编排步骤 1-3：fallback 补齐 sfx → 视频任务 → 混音。
// 与 Service/DB 解耦，便于对编排逻辑做表驱动单测。
func runStoryboardRowVideo(ctx context.Context, gw rowVideoGateway, params rowVideoParams) (rowVideoOutcome, error) {
	row := params.Row
	out := rowVideoOutcome{}
	// 存量行声音标注字段缺失时先走启发式 fallback 补齐（VoiceMode 仅为标注透传，无配音链）。
	fallback := computeStoryboardAudioFallback(strings.TrimSpace(row.Dialogue), len(row.Characters), row.AudioEffects)
	out.VoiceMode = firstNonEmpty(strings.TrimSpace(row.VoiceMode), fallback.VoiceMode)
	out.SfxTags = row.SfxTags
	if len(out.SfxTags) == 0 {
		out.SfxTags = fallback.SfxTags
	}
	prompt := firstNonEmpty(strings.TrimSpace(row.VideoMotionPrompt), strings.TrimSpace(row.VideoPrompt))
	if prompt == "" {
		return out, fmt.Errorf("分镜行缺少 videoMotionPrompt，无法生成视频")
	}

	out.Seconds = row.DurationSeconds
	if out.Seconds <= 0 {
		out.Seconds = 1
	}

	request := rowVideoJobRequest{Prompt: prompt, Seconds: out.Seconds}
	if params.Width > 0 && params.Height > 0 {
		request.Size = fmt.Sprintf("%dx%d", params.Width, params.Height)
	}
	if params.FirstFrameDataURL != "" {
		request.ReferenceImages = []string{params.FirstFrameDataURL}
	}
	videoJobID, err := gw.CreateVideoJob(ctx, request)
	if err != nil {
		return out, fmt.Errorf("提交视频任务失败：%w", err)
	}
	out.VideoJobID = videoJobID
	if _, err := waitStoryboardRowJob(ctx, gw, videoJobID); err != nil {
		return out, fmt.Errorf("视频生成失败（job %s）：%w", videoJobID, err)
	}

	tracks := make([]rowVideoMixTrack, 0, len(out.SfxTags))
	for _, tag := range out.SfxTags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		tracks = append(tracks, rowVideoMixTrack{SfxTag: tag, GainDB: 0, StartS: 0})
	}
	// 无 TTS 音源，混音不保留源音轨（视频本身无音频轨）。
	mixJobID, err := gw.CreateMixJob(ctx, videoJobID, tracks, false)
	if err != nil {
		return out, fmt.Errorf("提交混音失败：%w", err)
	}
	out.MixJobID = mixJobID
	mixResult, err := waitStoryboardRowJob(ctx, gw, mixJobID)
	if err != nil {
		return out, fmt.Errorf("混音失败（job %s）：%w", mixJobID, err)
	}
	out.MixOutputPath = mixResult.OutputPath
	if strings.TrimSpace(out.MixOutputPath) == "" {
		return out, fmt.Errorf("混音 job %s 未返回输出文件", mixJobID)
	}
	return out, nil
}

func (s *Service) resourceImageDataURL(userID string, resourceID string) (string, error) {
	resource, body, err := s.OpenResource(userID, resourceID)
	if err != nil {
		return "", err
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		return "", err
	}
	mimeType := resource.MimeType
	if strings.TrimSpace(mimeType) == "" {
		mimeType = "image/png"
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// --- gatewayClient 对 rowVideoGateway 的实现（video/mix 走任务轮询） ---

func (c *gatewayClient) CreateVideoJob(ctx context.Context, req rowVideoJobRequest) (string, error) {
	return c.postGatewayForID(ctx, "/v1/videos", req)
}

func (c *gatewayClient) CreateMixJob(ctx context.Context, videoJobID string, tracks []rowVideoMixTrack, keepSourceAudio bool) (string, error) {
	return c.postGatewayForID(ctx, "/v1/mix", map[string]any{
		"video_job_id": videoJobID, "tracks": tracks, "keep_source_audio": keepSourceAudio})
}

func (c *gatewayClient) WaitJob(ctx context.Context, jobID string) (rowVideoJobResult, error) {
	job, err := c.wait(ctx, jobID)
	if err != nil {
		return rowVideoJobResult{}, err
	}
	return rowVideoJobResult{OutputPath: firstNonEmpty(job.OutputPath, c.metaString(job, "output_path"))}, nil
}

func (c *gatewayClient) postGatewayForID(ctx context.Context, path string, body any) (string, error) {
	var created struct {
		ID string `json:"id"`
	}
	if err := c.getGatewayJSON(ctx, http.MethodPost, path, body, &created); err != nil {
		return "", err
	}
	if strings.TrimSpace(created.ID) == "" {
		return "", fmt.Errorf("gateway %s 未返回任务 id", path)
	}
	return created.ID, nil
}

func (c *gatewayClient) getGatewayJSON(ctx context.Context, method string, path string, body any, target any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("gateway %s: %s", response.Status, strings.TrimSpace(string(payload)))
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("解析 gateway %s 响应失败：%w：%s", path, err, strings.TrimSpace(string(payload)))
	}
	return nil
}

func (c *gatewayClient) downloadGatewayFile(ctx context.Context, url string, ext string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载 %s: %s", url, response.Status)
	}
	temp, err := os.CreateTemp("", "canvas-row-audio-*"+ext)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(temp, response.Body); err != nil {
		temp.Close()
		os.Remove(temp.Name())
		return "", err
	}
	if err := temp.Close(); err != nil {
		os.Remove(temp.Name())
		return "", err
	}
	return temp.Name(), nil
}
