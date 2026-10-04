package app

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

type fakeGateway struct {
	mu        sync.Mutex
	photos    []map[string]any
	nextID    int
	failShotN int // 从 1 数，第 N 个 shot job 返回 failed；0 表示全部成功
}

func (f *fakeGateway) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/jobs":
			var body struct {
				Type   string         `json:"type"`
				Params map[string]any `json:"params"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode job body: %v", err)
				http.Error(w, "bad body", http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			f.nextID++
			id := fmt.Sprintf("job-%d", f.nextID)
			status := "queued"
			if body.Type == "shot" && f.failShotN > 0 {
				shotCount := 0
				for _, previous := range f.photos {
					if previous["type"] == "shot" {
						shotCount++
					}
				}
				if shotCount+1 == f.failShotN {
					status = "failed"
				}
			}
			f.photos = append(f.photos, map[string]any{"id": id, "type": body.Type, "status": status, "params": body.Params, "path": r.URL.Path})
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"id":%q,"status":%q}`, id, status)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/concat":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode concat body: %v", err)
				http.Error(w, "bad body", http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			f.photos = append(f.photos, map[string]any{"id": "concat-1", "type": "concat", "status": "queued", "params": body, "path": r.URL.Path})
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"concat-1","job_id":"concat-1","status":"queued"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/music":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode music body: %v", err)
				http.Error(w, "bad body", http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			f.photos = append(f.photos, map[string]any{"id": "music-1", "type": "music", "status": "completed", "params": body})
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"url":%q,"duration_seconds":%v}`, "http://"+r.Host+"/music.wav", body["duration_s"])
		case r.Method == http.MethodGet && r.URL.Path == "/music.wav":
			w.Header().Set("Content-Type", "audio/wav")
			_, _ = w.Write([]byte("RIFF-fake-music"))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/jobs/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/jobs/")
			f.mu.Lock()
			var jobType, jobStatus string
			for _, entry := range f.photos {
				if entry["id"] == id {
					jobType, _ = entry["type"].(string)
					jobStatus, _ = entry["status"].(string)
				}
			}
			f.mu.Unlock()
			if jobType == "" {
				http.Error(w, "job not found", http.StatusNotFound)
				return
			}
			if jobStatus == "failed" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"id":%q,"status":"failed","error":"mock 渲染失败"}`, id)
				return
			}
			output := filepath.ToSlash(filepath.Join(t.TempDir(), id+"-shot.mp4"))
			lastFrame := filepath.ToSlash(filepath.Join(t.TempDir(), id+"-last.png"))
			if err := os.WriteFile(filepath.FromSlash(output), []byte("mp4"), 0o600); err != nil {
				t.Errorf("write mock output: %v", err)
			}
			if err := os.WriteFile(filepath.FromSlash(lastFrame), []byte("png"), 0o600); err != nil {
				t.Errorf("write mock last frame: %v", err)
			}
			completed := `{"id":%q,"status":"completed","output_path":%q,"meta":{"last_frame_path":%q}}`
			args := []any{id, output, lastFrame}
			if jobType == "concat" {
				completed = `{"id":%q,"status":"completed","output_path":%q,"meta":{"count":2}}`
				args = args[:2]
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, completed, args...)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	})
}

func (f *fakeGateway) submitted(t *testing.T, jobType string) []map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]map[string]any, 0, 2)
	for _, entry := range f.photos {
		if entry["type"] == jobType {
			params, _ := entry["params"].(map[string]any)
			result = append(result, params)
		}
	}
	return result
}

// submittedAt 是 submitted 加上 endpoint 断言。submitted() 只按 job type 找记录，
// 而 /v1/jobs 的 stub 也会把 {"type":"concat","params":{...}} 记成 type=concat ——
// 两者形状相同，光看 payload 分不出走的哪条路由。要卡住"成片走 /v1/concat 而不是
// /v1/jobs"必须比对 path。
func (f *fakeGateway) submittedAt(t *testing.T, jobType, path string) []map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var result []map[string]any
	for _, entry := range f.photos {
		if entry["type"] == jobType && entry["path"] == path {
			params, _ := entry["params"].(map[string]any)
			result = append(result, params)
		}
	}
	if len(result) == 0 {
		var seen []string
		for _, entry := range f.photos {
			if entry["type"] == jobType {
				seen = append(seen, fmt.Sprintf("%v", entry["path"]))
			}
		}
		t.Fatalf("%s jobs via %s = 0, want >=1 (实际提交到: %v)", jobType, path, seen)
	}
	return result
}

func newRenderAllTestService(t *testing.T, aspectRatio ...string) (*Service, *fakeGateway) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+newID()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.Project{}, &model.Shot{}, &model.ShotRevision{}, &model.ShotArtifact{}, &model.ShotAssetReference{},
		&model.Resource{}, &model.UserOSSSetting{}, &model.SystemSetting{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := db.Create(&model.Project{ID: "project-render", UserID: "user-render", Name: "连续渲染测试", AspectRatio: aspectRatioValue(aspectRatio), Status: model.ProjectStatusActive, Revision: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	shots := []model.Shot{
		{ID: "shot-1", ProjectID: "project-render", UnitID: "unit-1", CurrentRevisionID: "rev-1", Title: "开场", Position: 0, DurationMs: 4000, CreatedAt: now, UpdatedAt: now},
		{ID: "shot-2", ProjectID: "project-render", UnitID: "unit-1", CurrentRevisionID: "rev-2", Title: "追逐", Position: 1, DurationMs: 5000, CreatedAt: now, UpdatedAt: now},
	}
	revisions := []model.ShotRevision{
		{ID: "rev-1", ShotID: "shot-1", Version: 1, PlotDescription: "雨夜开场", Dialogue: "谁在那里？", DurationMs: 4000, ImagePrompt: "雨夜街道", VideoPrompt: "镜头推进", CreatedAt: now},
		{ID: "rev-2", ShotID: "shot-2", Version: 1, PlotDescription: "主角奔跑", DurationMs: 5000, ImagePrompt: "追逐街道", VideoPrompt: "跟随奔跑", CreatedAt: now},
	}
	for index := range shots {
		if err := db.Create(&shots[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	for index := range revisions {
		if err := db.Create(&revisions[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	gateway := &fakeGateway{}
	server := httptest.NewServer(gateway.handler(t))
	t.Cleanup(server.Close)
	service := &Service{repo: repository.New(db), dataDir: t.TempDir(), mediaGatewayClient: &gatewayClient{baseURL: server.URL, httpClient: server.Client(), pollInterval: 10 * time.Millisecond, jobTimeout: 5 * time.Second}}
	return service, gateway
}

func TestRenderAllProjectShotsChainsLastFrameAndRegistersFinal(t *testing.T) {
	service, gateway := newRenderAllTestService(t)
	result, err := service.RenderAllProjectShots(context.Background(), "user-render", "project-render", RenderAllShotsRequest{MusicPrompt: "紧张配乐"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Shots) != 2 {
		t.Fatalf("rendered shots = %d, want 2", len(result.Shots))
	}
	shotJobs := gateway.submitted(t, "shot")
	if len(shotJobs) != 2 {
		t.Fatalf("shot jobs = %d, want 2", len(shotJobs))
	}
	firstVideo := shotJobs[0]["video"].(map[string]any)
	if firstVideo["first_frame"] != "auto" {
		t.Fatalf("first shot first_frame = %v, want auto", firstVideo["first_frame"])
	}
	if _, exists := shotJobs[0]["image"].(map[string]any); !exists {
		t.Fatal("first shot missing image stage")
	}
	if firstVideo["seconds"] != float64(4) {
		t.Fatalf("first shot seconds = %v, want 4", firstVideo["seconds"])
	}
	if _, hasImage := shotJobs[1]["image"]; hasImage {
		t.Fatal("second shot must not have image stage")
	}
	secondVideo := shotJobs[1]["video"].(map[string]any)
	if secondVideo["first_frame"] != result.Shots[0].LastFramePath || result.Shots[0].LastFramePath == "" {
		t.Fatalf("second shot first_frame = %v, want previous last frame %q", secondVideo["first_frame"], result.Shots[0].LastFramePath)
	}
	if firstVideo["refs"] != nil {
		t.Fatalf("unexpected refs on first shot: %v", firstVideo["refs"])
	}
	if _, hasVoice := shotJobs[0]["voice"]; hasVoice {
		t.Fatal("shot with dialogue but no voice binding must skip voice stage")
	}
	if len(result.VoiceSkippedShotIDs) != 1 || result.VoiceSkippedShotIDs[0] != "shot-1" {
		t.Fatalf("voiceSkippedShotIds = %v, want [shot-1]", result.VoiceSkippedShotIDs)
	}
	// 配乐改成成片级：逐镜不再各带一段，否则与 concat 的 music_segments 叠音。
	for index, params := range shotJobs {
		if music, hasMusic := params["music"]; hasMusic {
			t.Fatalf("shot %d 不应再带 music 段：%v", index, music)
		}
	}
	musicReqs := gateway.submitted(t, "music")
	// 成片 9s < Gateway /v1/music 的下限 10s：不 clamp 的话网关直接 400，
	// 而 render-all 把它变成一个不透出原因的 500（真机跑出来的）。
	if len(musicReqs) != 1 || musicReqs[0]["prompt"] != "紧张配乐" || musicReqs[0]["duration_s"] != float64(10) {
		t.Fatalf("成片配乐请求 = %v, want 1 条 10s（9s 成片 clamp 到下限）", musicReqs)
	}

	concatJobs := gateway.submittedAt(t, "concat", "/v1/concat")
	if len(concatJobs) != 1 {
		t.Fatalf("concat jobs = %d, want 1", len(concatJobs))
	}
	paths, _ := concatJobs[0]["shots"].([]any)
	if len(paths) != 2 || paths[0] != result.Shots[0].VideoPath || paths[1] != result.Shots[1].VideoPath {
		t.Fatalf("concat shots = %v, want rendered video paths in order", paths)
	}
	segments, _ := concatJobs[0]["music_segments"].([]any)
	if len(segments) != 1 {
		t.Fatalf("concat music_segments = %v, want 1 段", concatJobs[0]["music_segments"])
	}
	segment, _ := segments[0].(map[string]any)
	wavPath, _ := segment["path"].(string)
	if wavPath == "" {
		t.Fatalf("concat music segment path = %v", segment)
	}
	if _, statErr := os.Stat(wavPath); statErr != nil {
		t.Fatalf("配乐 wav 应已下载到本地：%v", statErr)
	}
	if concatJobs[0]["bgm_gain_db"] != renderAllBgmGainDB {
		t.Fatalf("concat bgm_gain_db = %v, want %v", concatJobs[0]["bgm_gain_db"], renderAllBgmGainDB)
	}
	if result.ConcatJobID == "" || result.FinalResourceID == "" {
		t.Fatalf("concatJobId=%q finalResourceId=%q, want both set", result.ConcatJobID, result.FinalResourceID)
	}
	resource, err := service.repo.ResourceForUser("user-render", result.FinalResourceID)
	if err != nil || resource.Status != model.ResourceStatusReady || resource.Kind != "video" {
		t.Fatalf("final resource = %+v err=%v, want ready video", resource, err)
	}
	if _, statErr := os.Stat(filepath.Join(service.dataDir, "resources", filepath.FromSlash(resource.ObjectKey))); statErr != nil {
		t.Fatalf("final resource object missing: %v", statErr)
	}

	artifacts, err := service.repo.ProjectShotArtifacts("project-render")
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 4 {
		t.Fatalf("artifacts = %d, want 4 (video + shot_last_frame per shot)", len(artifacts))
	}
	for _, artifact := range artifacts {
		if artifact.Status != "ready" || !artifact.Selected || artifact.Version != 1 {
			t.Fatalf("artifact %s/%s status=%s selected=%v version=%d", artifact.ShotID, artifact.Type, artifact.Status, artifact.Selected, artifact.Version)
		}
		var metadata map[string]string
		if err := json.Unmarshal([]byte(artifact.MetadataJSON), &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata["gatewayJobId"] == "" {
			t.Fatalf("artifact %s metadata missing gatewayJobId", artifact.Type)
		}
	}
	updatedShots, err := service.repo.ProjectShots("project-render")
	if err != nil {
		t.Fatal(err)
	}
	for _, shot := range updatedShots {
		if shot.Status != "completed" {
			t.Fatalf("shot %s status = %q, want completed", shot.ID, shot.Status)
		}
	}
}

func TestMatchAutoCharacterAssets(t *testing.T) {
	ice := model.Asset{ID: "asset-ice", Category: model.AssetCategoryCharacter, PrimaryVersionID: "ice-draft", Title: "冰法师霜璃"}
	fire := model.Asset{ID: "asset-fire", Category: model.AssetCategoryCharacter, PrimaryVersionID: "fire-v1", Title: "火法师焰璃"}
	snow := model.Asset{ID: "asset-snow", Category: model.AssetCategoryCharacter, PrimaryVersionID: "snow-v1", Title: "冰法师雪"}
	prop := model.Asset{ID: "asset-prop", Category: model.AssetCategoryProp, PrimaryVersionID: "prop-v1", Title: "霜璃雕像"}

	tests := []struct {
		name    string
		text    string
		assets  []model.Asset
		bound   map[string]bool
		wantIDs []string
	}{
		{name: "完整title命中", text: "中景：冰法师霜璃 特写", assets: []model.Asset{ice}, wantIDs: []string{"asset-ice"}},
		{name: "短名后缀命中", text: "霜璃 转身离去", assets: []model.Asset{ice}, wantIDs: []string{"asset-ice"}},
		{name: "称号前缀命中", text: "冰法师 抬手结霜", assets: []model.Asset{ice}, wantIDs: []string{"asset-ice"}},
		{name: "短名只命中火", text: "焰璃 怒吼，冰霜消融", assets: []model.Asset{ice, fire}, wantIDs: []string{"asset-fire"}},
		{name: "中缀泛称不命中任何一个", text: "两位 法师 对峙", assets: []model.Asset{ice, fire}, wantIDs: nil},
		{name: "短词元多命中全失效", text: "冰法 咒语回荡", assets: []model.Asset{ice, snow}, wantIDs: nil},
		{name: "短词元唯一命中有效", text: "霜璃 咒语回荡", assets: []model.Asset{ice, snow}, wantIDs: []string{"asset-ice"}},
		{name: "已绑定不重复补挂", text: "冰法师霜璃 与 火法师焰璃", assets: []model.Asset{ice, fire}, bound: map[string]bool{"asset-fire": true}, wantIDs: []string{"asset-ice"}},
		{name: "非角色类别不参与", text: "霜璃雕像 特写", assets: []model.Asset{prop}, wantIDs: nil},
		{name: "无关文本不命中", text: "空镜：雪原日落", assets: []model.Asset{ice}, wantIDs: nil},
		{name: "单字核心名不参与", text: "他拿出 月 光石", assets: []model.Asset{{ID: "m", Category: model.AssetCategoryCharacter, Title: "月"}}, wantIDs: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bound := test.bound
			if bound == nil {
				bound = map[string]bool{}
			}
			matched := matchAutoCharacterAssets(model.AssetCandidateNameKey(test.text), test.assets, bound)
			gotIDs := make([]string, 0, len(matched))
			for _, asset := range matched {
				gotIDs = append(gotIDs, asset.ID)
			}
			if len(gotIDs) != len(test.wantIDs) {
				t.Fatalf("matched = %v, want %v", gotIDs, test.wantIDs)
			}
			for index := range gotIDs {
				if gotIDs[index] != test.wantIDs[index] {
					t.Fatalf("matched = %v, want %v", gotIDs, test.wantIDs)
				}
			}
		})
	}
}

func TestRenderAllProjectShotsStopsOnFailure(t *testing.T) {
	service, _ := newRenderAllTestService(t)
	gateway := &fakeGateway{failShotN: 2}
	server := httptest.NewServer(gateway.handler(t))
	defer server.Close()
	service.mediaGatewayClient = &gatewayClient{baseURL: server.URL, httpClient: server.Client(), pollInterval: 10 * time.Millisecond, jobTimeout: 5 * time.Second}

	_, err := service.RenderAllProjectShots(context.Background(), "user-render", "project-render", RenderAllShotsRequest{})
	if err == nil || !strings.Contains(err.Error(), "追逐") {
		t.Fatalf("err = %v, want failure naming shot 追逐", err)
	}
	if len(gateway.submitted(t, "concat")) != 0 {
		t.Fatal("concat must not run after shot failure")
	}
	shots, err := service.repo.ProjectShots("project-render")
	if err != nil {
		t.Fatal(err)
	}
	if shots[0].Status != "completed" {
		t.Fatalf("completed shot status = %q, artifacts must survive failure", shots[0].Status)
	}
}

// 首帧曾落 qwen_image 缺省 1024x1024（实测死锁尺寸），且与视频画幅不一致。
// 这条锁住「首帧与视频同幅、且按项目 aspectRatio 推导」。
func TestRenderAllProjectShotsDerivesSizeFromAspectRatio(t *testing.T) {
	service, gateway := newRenderAllTestService(t, "9:16")
	if _, err := service.RenderAllProjectShots(context.Background(), "user-render", "project-render", RenderAllShotsRequest{}); err != nil {
		t.Fatal(err)
	}
	jobs := gateway.submitted(t, "shot")
	image, ok := jobs[0]["image"].(map[string]any)
	if !ok {
		t.Fatal("first shot missing image stage")
	}
	video := jobs[0]["video"].(map[string]any)
	if fmt.Sprint(image["width"]) != "480" || fmt.Sprint(image["height"]) != "864" {
		t.Fatalf("image size = %vx%v, want 480x864", image["width"], image["height"])
	}
	if fmt.Sprint(video["width"]) != "480" || fmt.Sprint(video["height"]) != "864" {
		t.Fatalf("video size = %vx%v, want 480x864", video["width"], video["height"])
	}
}

// 显式传入的画幅优先于项目设置。
func TestRenderAllProjectShotsHonoursExplicitSize(t *testing.T) {
	service, gateway := newRenderAllTestService(t)
	_, err := service.RenderAllProjectShots(context.Background(), "user-render", "project-render",
		RenderAllShotsRequest{Width: 1024, Height: 576})
	if err != nil {
		t.Fatal(err)
	}
	video := gateway.submitted(t, "shot")[0]["video"].(map[string]any)
	if fmt.Sprint(video["width"]) != "1024" || fmt.Sprint(video["height"]) != "576" {
		t.Fatalf("video size = %vx%v, want 1024x576", video["width"], video["height"])
	}
}

func aspectRatioValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// 草稿是纯 h3 快档，全程无音乐：不调 /v1/music、逐镜不带 music 段、成片也不铺。
func TestRenderAllProjectShotsDraftStaysMusicFree(t *testing.T) {
	service, gateway := newRenderAllTestService(t)
	if _, err := service.RenderAllProjectShots(context.Background(), "user-render", "project-render", RenderAllShotsRequest{MusicPrompt: "紧张配乐", Draft: true}); err != nil {
		t.Fatal(err)
	}
	if len(gateway.submitted(t, "music")) != 0 {
		t.Fatal("草稿不应生成配乐")
	}
	for index, params := range gateway.submitted(t, "shot") {
		if music, hasMusic := params["music"]; hasMusic {
			t.Fatalf("草稿 shot %d 不应有 music 段：%v", index, music)
		}
	}
	concatJobs := gateway.submittedAt(t, "concat", "/v1/concat")
	if len(concatJobs) != 1 {
		t.Fatalf("concat jobs = %d, want 1", len(concatJobs))
	}
	if segments, hasSegments := concatJobs[0]["music_segments"]; hasSegments {
		t.Fatalf("草稿成片不应带 music_segments：%v", segments)
	}
}

// 只渲一个镜头时没有成片可铺，逐镜自带配乐必须留着（RenderProjectShot 走同一条路）。
func TestRenderAllProjectShotsSingleShotKeepsShotMusic(t *testing.T) {
	service, gateway := newRenderAllTestService(t)
	if _, err := service.RenderAllProjectShots(context.Background(), "user-render", "project-render", RenderAllShotsRequest{MusicPrompt: "紧张配乐", FromShotID: "shot-2"}); err != nil {
		t.Fatal(err)
	}
	if len(gateway.submitted(t, "concat")) != 0 {
		t.Fatal("单镜头不应提交 concat")
	}
	if len(gateway.submitted(t, "music")) != 0 {
		t.Fatal("单镜头不应额外生成成片配乐")
	}
	music, ok := gateway.submitted(t, "shot")[0]["music"].(map[string]any)
	if !ok || music["prompt"] != "紧张配乐" || music["duration_s"] != float64(5) {
		t.Fatalf("单镜头应保留逐镜 music 段：%v", music)
	}
}

func TestRenderProjectShotReportsVoiceSkipped(t *testing.T) {
	// 單鏡頭渲染此前把 renderProjectShot 的第三個返回值用 _ 丟掉，於是帶台詞的
	// 鏡頭被靜默丟棄台詞、API 也不回報任何東西（render-all 有 voiceSkippedShotIds）。
	service, gateway := newRenderAllTestService(t)
	// Draft 會整段跳過 voice（那是 draft 的定義），所以要跑完整鏈路才碰得到這條路徑。
	result, err := service.RenderProjectShot(context.Background(), "user-render", "project-render", "shot-1", RenderAllShotsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	// shot-1 的 revision 帶 Dialogue，且沒有任何音色綁定 → 必須回報
	if !result.VoiceSkipped {
		t.Fatal("voiceSkipped must be true for a dialogue shot with no voice binding")
	}
	if _, hasVoice := gateway.submitted(t, "shot")[0]["voice"]; hasVoice {
		t.Fatal("no voice stage may be sent when no voice binding exists")
	}
}
