package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// 验证 newapi(OpenAI Videos) multipart：全部参考图逐值进 input_images，首帧/静音透传。
func TestRunVideoTaskNewAPISendsAllImagesAndFrameFields(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	center, err := newPluginRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var formValues map[string][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/videos":
			if err := r.ParseMultipartForm(1 << 24); err != nil {
				t.Errorf("ParseMultipartForm() error = %v", err)
				http.Error(w, "bad form", http.StatusBadRequest)
				return
			}
			mu.Lock()
			formValues = r.MultipartForm.Value
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"video-1","status":"completed"}`))
		case "/v1/videos/video-1/content":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("video"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	mute := false
	ctx := withProtocolRegistry(context.Background(), center.registrySnapshot())
	_, err = runVideoTask(ctx, canvasGenerationInput{
		Mode: "video", Prompt: "cinematic shot",
		Config:          providerConfig{BaseURL: server.URL, APIKey: "test-key", InterfaceType: "newapi", Model: "sora-2", VideoSeconds: "5"},
		ReferenceImages: []providerMedia{{ID: "a", URL: "https://example.com/a.png"}, {ID: "b", URL: server.URL + "/b.png"}, {ID: "c", DataURL: testReferenceImageDataURL}},
		FirstFrameImage: "data:image/png;base64,AAAA",
		MuteAudio:       &mute,
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := formValues["input_images"]; len(got) != 3 || got[0] != "https://example.com/a.png" || got[2] != testReferenceImageDataURL {
		t.Fatalf("input_images = %#v, want 3 张参考图原样", got)
	}
	if got := formValues["first_frame_image"]; len(got) != 1 || got[0] != "data:image/png;base64,AAAA" {
		t.Fatalf("first_frame_image = %#v", got)
	}
	if got := formValues["mute_audio"]; len(got) != 1 || got[0] != "false" {
		t.Fatalf("mute_audio = %#v, want [\"false\"]", got)
	}
	if _, exists := formValues["input_reference"]; exists {
		t.Fatalf("遗留字段 input_reference 不应再发送：%#v", formValues)
	}
}

// 验证 referenceImages 真身在 storageKey（dataUrl 为空串）的跨端契约：
// resource: 引用先物化成 dataURL 再进 input_images，全空条目必须被过滤
// （Gateway 对无法解析的值直接 400，一个空串就毁掉整个任务）。
func TestRunVideoTaskMaterializesStorageKeyReferences(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	service, _, db := newAudioPostProcessTestService(t)
	objectKey := "images/users/user-avp/ref.png"
	if err := os.MkdirAll(filepath.Join(service.dataDir, "resources", filepath.Dir(objectKey)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(service.dataDir, "resources", objectKey), []byte("png-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := db.Create(&model.Resource{ID: "img-avp", UserID: "user-avp", Kind: "image", Status: model.ResourceStatusReady, Provider: "local", ObjectKey: objectKey, MimeType: "image/png", Size: 9, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var inputImages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/videos":
			if err := r.ParseMultipartForm(1 << 24); err != nil {
				http.Error(w, "bad form", http.StatusBadRequest)
				return
			}
			mu.Lock()
			inputImages = r.MultipartForm.Value["input_images"]
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"video-1","status":"completed"}`))
		case "/v1/videos/video-1/content":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("video"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	input := canvasGenerationInput{
		Mode: "video", Prompt: "镜头",
		Config:          providerConfig{BaseURL: server.URL, APIKey: "test-key", InterfaceType: "newapi", Model: "sora-2"},
		ReferenceImages: []providerMedia{{ID: "r1", Type: "image/png", StorageKey: "resource:img-avp"}, {ID: "blank"}},
	}
	if err := service.hydrateGenerationMedia("user-avp", &input, false); err != nil {
		t.Fatal(err)
	}
	if _, err := runVideoTask(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(inputImages) != 1 {
		t.Fatalf("input_images = %#v, want 恰好 1 个（storageKey 物化 + 空条目过滤）", inputImages)
	}
	if !strings.HasPrefix(inputImages[0], "data:image/png;base64,") {
		t.Fatalf("input_images[0] 应为物化后的 dataURL，实际 %q", inputImages[0])
	}
}

// 验证 canvas_video 音频后处理：voice→music→mix 链路成功后混音成片成为最终产物，原视频保留。
func TestCanvasVideoAudioPostProcessMixesVoiceAndMusic(t *testing.T) {
	service, gateway, db := newAudioPostProcessTestService(t)
	task := seedAudioPostProcessTask(t, db, `{"mode":"video","prompt":"镜头","config":{"videoSeconds":"4"},"metadata":{"assetVersionId":"ver-1"}}`, "台词", "紧张配乐")

	gateway.mu.Lock()
	gateway.voiceKey = "narrator-main"
	gateway.mu.Unlock()
	service.maybeScheduleCanvasVideoAudioPostProcess(task, context.Background())

	waitForAudioPostProcess(t, service, task.ID, "audioPostProcess")
	latest, err := service.repo.Task(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(latest.ResultJSON, "postProcessError") {
		t.Fatalf("不应有 postProcessError：%s", latest.ResultJSON)
	}
	if latest.Status != model.TaskStatusSucceeded {
		t.Fatalf("status = %q, want succeeded", latest.Status)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(latest.ResultJSON), &result); err != nil {
		t.Fatal(err)
	}
	video, _ := result["video"].(map[string]any)
	if video == nil || video["resourceId"] == "resource-avp" || strings.TrimSpace(fmt.Sprint(video["resourceId"])) == "" {
		t.Fatalf("最终产物应为混音新资源，实际 %v", video)
	}
	post, _ := result["audioPostProcess"].(map[string]any)
	if post == nil || fmt.Sprint(post["mixOutputPath"]) == "" {
		t.Fatalf("缺少 audioPostProcess 元数据：%v", post)
	}
	source, _ := post["sourceVideo"].(map[string]any)
	if source == nil || source["resourceId"] != "resource-avp" {
		t.Fatalf("原视频应保留为 sourceVideo：%v", source)
	}

	jobs := gateway.submitted(t)
	if len(jobs) != 3 {
		t.Fatalf("jobs = %d, want voice+music+mix", len(jobs))
	}
	voice := jobs["voice"]
	if voice["voice"] != "narrator-main" || voice["text"] != "台词" {
		t.Fatalf("voice params = %v", voice)
	}
	music := jobs["music"]
	if music["prompt"] != "紧张配乐" || music["duration_s"] != float64(4) {
		t.Fatalf("music params = %v", music)
	}
	mix := jobs["mix"]
	if mix["output_name"] != "final.mp4" {
		t.Fatalf("mix params = %v", mix)
	}
	if !strings.HasSuffix(fmt.Sprint(mix["video"]), "source.mp4") {
		t.Fatalf("mix video 应指向本地视频文件：%v", mix["video"])
	}
	tracks, _ := mix["audio_tracks"].([]any)
	if len(tracks) != 2 {
		t.Fatalf("audio_tracks = %v", mix["audio_tracks"])
	}
	voiceTrack, _ := tracks[0].(map[string]any)
	musicTrack, _ := tracks[1].(map[string]any)
	if voiceTrack["path"] != gateway.outputs["voice"] || voiceTrack["start"] != float64(0) {
		t.Fatalf("voice track = %v", voiceTrack)
	}
	if musicTrack["path"] != gateway.outputs["music"] || musicTrack["loop"] != true {
		t.Fatalf("music track = %v", musicTrack)
	}
}

// 验证失败路径：voice job 失败时任务保持成功、产物不变，错误写 postProcessError。
func TestCanvasVideoAudioPostProcessFailureKeepsOriginalVideo(t *testing.T) {
	service, gateway, db := newAudioPostProcessTestService(t)
	gateway.mu.Lock()
	gateway.voiceKey = "narrator-main"
	gateway.failType = "voice"
	gateway.mu.Unlock()
	task := seedAudioPostProcessTask(t, db, `{"mode":"video","prompt":"镜头","dialogue":"台词","metadata":{"assetVersionId":"ver-1"}}`, "台词", "")

	service.maybeScheduleCanvasVideoAudioPostProcess(task, context.Background())
	waitForAudioPostProcess(t, service, task.ID, "postProcessError")
	latest, err := service.repo.Task(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(latest.ResultJSON), &result); err != nil {
		t.Fatal(err)
	}
	if _, ok := result["postProcessError"].(string); !ok {
		t.Fatalf("缺少 postProcessError：%s", latest.ResultJSON)
	}
	video, _ := result["video"].(map[string]any)
	if video == nil || video["resourceId"] != "resource-avp" {
		t.Fatalf("产物应保持原视频：%v", video)
	}
	if _, ok := result["audioPostProcess"]; ok {
		t.Fatalf("失败时不应写入 audioPostProcess：%v", result["audioPostProcess"])
	}
}

// 验证无对白无 BGM 的任务零行为变化：不触发任何 gateway job。
func TestCanvasVideoAudioPostProcessSkipsPlainTasks(t *testing.T) {
	service, gateway, db := newAudioPostProcessTestService(t)
	task := seedAudioPostProcessTask(t, db, `{"mode":"video","prompt":"镜头"}`, "", "")

	service.maybeScheduleCanvasVideoAudioPostProcess(task, context.Background())
	time.Sleep(50 * time.Millisecond)
	if jobs := gateway.submitted(t); len(jobs) != 0 {
		t.Fatalf("plain 任务不应触发后处理 jobs：%v", jobs)
	}
}

// --- 测试脚手架 ---

type audioPostProcessGateway struct {
	mu       sync.Mutex
	jobs     []map[string]any
	outputs  map[string]string
	voiceKey string
	failType string
	nextID   int
}

func (g *audioPostProcessGateway) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/jobs":
			var body struct {
				Type   string         `json:"type"`
				Params map[string]any `json:"params"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad body", http.StatusBadRequest)
				return
			}
			g.mu.Lock()
			g.nextID++
			id := fmt.Sprintf("job-%d", g.nextID)
			status := "queued"
			if body.Type == g.failType {
				status = "failed"
			}
			g.jobs = append(g.jobs, map[string]any{"id": id, "type": body.Type, "status": status, "params": body.Params})
			g.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"id":%q,"status":%q}`, id, status)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/jobs/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/jobs/")
			g.mu.Lock()
			var jobType, jobStatus string
			for _, entry := range g.jobs {
				if entry["id"] == id {
					jobType, _ = entry["type"].(string)
					jobStatus, _ = entry["status"].(string)
				}
			}
			g.mu.Unlock()
			if jobType == "" {
				http.Error(w, "job not found", http.StatusNotFound)
				return
			}
			if jobStatus == "failed" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"id":%q,"status":"failed","error":"mock %s 失败"}`, id, jobType)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"id":%q,"status":"completed","output_path":%q}`, id, g.output(t, jobType))
		default:
			http.NotFound(w, r)
		}
	})
}

func (g *audioPostProcessGateway) output(t *testing.T, jobType string) string {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.outputs == nil {
		g.outputs = map[string]string{}
	}
	if path, ok := g.outputs[jobType]; ok {
		return path
	}
	ext := ".mp3"
	if jobType == "mix" {
		ext = ".mp4"
	}
	path := filepath.ToSlash(filepath.Join(t.TempDir(), jobType+"-output"+ext))
	if err := os.WriteFile(filepath.FromSlash(path), []byte(jobType), 0o600); err != nil {
		t.Fatalf("write mock output: %v", err)
	}
	g.outputs[jobType] = path
	return path
}

func (g *audioPostProcessGateway) submitted(t *testing.T) map[string]map[string]any {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	result := map[string]map[string]any{}
	for _, entry := range g.jobs {
		params, _ := entry["params"].(map[string]any)
		jobType, _ := entry["type"].(string)
		result[jobType] = params
	}
	return result
}

func newAudioPostProcessTestService(t *testing.T) (*Service, *audioPostProcessGateway, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+newID()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Task{}, &model.TaskLog{}, &model.Resource{}, &model.VoiceProfile{}, &model.CharacterVoiceBinding{}, &model.UserOSSSetting{}, &model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	gateway := &audioPostProcessGateway{}
	server := httptest.NewServer(gateway.handler(t))
	t.Cleanup(server.Close)
	service := &Service{repo: repository.New(db), dataDir: t.TempDir(), mediaGatewayClient: &gatewayClient{baseURL: server.URL, httpClient: server.Client(), pollInterval: 10 * time.Millisecond, jobTimeout: 5 * time.Second}}
	// 原视频资源：本地存储真实文件，gateway 混音要读路径。
	objectKey := "videos/users/user-avp/source.mp4"
	if err := os.MkdirAll(filepath.Join(service.dataDir, "resources", filepath.Dir(objectKey)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(service.dataDir, "resources", objectKey), []byte("mp4"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := db.Create(&model.Resource{ID: "resource-avp", UserID: "user-avp", Kind: "video", Status: model.ResourceStatusReady, Provider: "local", ObjectKey: objectKey, MimeType: "video/mp4", Size: 3, DurationMs: 4000, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.VoiceProfile{ID: "voice-profile-1", UserID: "user-avp", Name: "旁白", Provider: "gateway", VoiceKey: "narrator-main", Status: "active", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.CharacterVoiceBinding{ID: "binding-1", AssetVersionID: "ver-1", VoiceProfileID: "voice-profile-1", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	return service, gateway, db
}

func seedAudioPostProcessTask(t *testing.T, db *gorm.DB, inputJSON string, dialogue string, bgm string) model.Task {
	t.Helper()
	result := map[string]any{"mode": "video", "video": map[string]any{"resourceId": "resource-avp", "url": "/api/resources/resource-avp/file", "storageKey": "resource:resource-avp"}}
	resultJSON, _ := json.Marshal(result)
	input := map[string]any{}
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		t.Fatal(err)
	}
	if dialogue != "" {
		input["dialogue"] = dialogue
	}
	if bgm != "" {
		input["bgm_prompt"] = bgm
	}
	inputJSONBytes, _ := json.Marshal(input)
	now := time.Now()
	task := model.Task{ID: newID(), UserID: "user-avp", Type: "canvas_video", Status: model.TaskStatusSucceeded, Stage: "任务完成", Progress: 100, Prompt: "镜头", InputJSON: string(inputJSONBytes), ResultJSON: string(resultJSON), CompletedAt: &now, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	return task
}

func waitForAudioPostProcess(t *testing.T, service *Service, taskID string, marker string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		task, err := service.repo.Task(taskID)
		if err == nil && strings.Contains(task.ResultJSON, marker) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待 %s 超时", marker)
}
