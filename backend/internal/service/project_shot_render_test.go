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
			f.photos = append(f.photos, map[string]any{"id": id, "type": body.Type, "status": status, "params": body.Params})
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"id":%q,"status":%q}`, id, status)
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

func newRenderAllTestService(t *testing.T) (*Service, *fakeGateway) {
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
	if err := db.Create(&model.Project{ID: "project-render", UserID: "user-render", Name: "连续渲染测试", Status: model.ProjectStatusActive, Revision: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	shots := []model.Shot{
		{ID: "shot-1", ProjectID: "project-render", UnitID: "unit-1", CurrentRevisionID: "rev-1", Title: "开场", Position: 0, DurationMs: 4000, CreatedAt: now, UpdatedAt: now},
		{ID: "shot-2", ProjectID: "project-render", UnitID: "unit-1", CurrentRevisionID: "rev-2", Title: "追逐", Position: 1, DurationMs: 5000, CreatedAt: now, UpdatedAt: now},
	}
	revisions := []model.ShotRevision{
		{ID: "rev-1", ShotID: "shot-1", Version: 1, PlotDescription: "雨夜开场", Dialogue: "谁在那里？", DurationMs: 4000, ImagePrompt: "雨夜街道", VideoPrompt: "镜头推进", CreatedAt: now},
		{ID: "rev-2", ShotID: "shot-2", Version: 1, PlotDescription: "主角奔跑", DurationMs: 5000, VideoPrompt: "跟随奔跑", CreatedAt: now},
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
	music := shotJobs[0]["music"].(map[string]any)
	if music["prompt"] != "紧张配乐" || music["duration_s"] != float64(4) {
		t.Fatalf("first shot music = %v", music)
	}

	concatJobs := gateway.submitted(t, "concat")
	if len(concatJobs) != 1 {
		t.Fatalf("concat jobs = %d, want 1", len(concatJobs))
	}
	paths, _ := concatJobs[0]["shots"].([]any)
	if len(paths) != 2 || paths[0] != result.Shots[0].VideoPath || paths[1] != result.Shots[1].VideoPath {
		t.Fatalf("concat shots = %v, want rendered video paths in order", paths)
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
	ice := model.Asset{ID: "asset-ice", Category: model.AssetCategoryCharacter, PrimaryVersionID: "ice-draft", Title: "冰魔法师（霜璃）"}
	fire := model.Asset{ID: "asset-fire", Category: model.AssetCategoryCharacter, PrimaryVersionID: "fire-v1", Title: "火法师"}
	prop := model.Asset{ID: "asset-prop", Category: model.AssetCategoryProp, PrimaryVersionID: "prop-v1", Title: "霜璃雕像"}
	bound := map[string]bool{"asset-fire": true}

	matched := matchAutoCharacterAssets(model.AssetCandidateNameKey("中景：冰魔法师（霜璃）与 火法师 对峙，衣摆结霜"), []model.Asset{ice, fire, prop}, bound)
	if len(matched) != 1 || matched[0].ID != "asset-ice" {
		t.Fatalf("matched = %+v, want only asset-ice (fire already bound, prop wrong category)", matched)
	}
	if len(matchAutoCharacterAssets(model.AssetCandidateNameKey("空镜：雪原日落"), []model.Asset{ice}, bound)) != 0 {
		t.Fatal("text without character title must not match")
	}
	if len(matchAutoCharacterAssets(model.AssetCandidateNameKey("他拿出 月 光石"), []model.Asset{{ID: "m", Category: model.AssetCategoryCharacter, Title: "月"}}, bound)) != 0 {
		t.Fatal("single-rune core name must not match")
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
