package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"infinite-canvas/backend/internal/model"
)

// fakeBatchStore 模拟子任务队列与 worker 领取：创建即按配置落到终态，不打真库。
type fakeBatchStore struct {
	createErrs map[int]error            // 行下标 → 创建失败
	outcomes   map[int]model.TaskStatus // 行下标 → 子任务终态
	created    []StoryboardRowVideoTaskRequest
	progress   []int
	stages     []string
	seq        int
	tasks      map[string]*model.Task
}

func (f *fakeBatchStore) CreateRowVideoTask(req StoryboardRowVideoTaskRequest) (*model.Task, error) {
	index := len(f.created)
	f.created = append(f.created, req)
	if err, ok := f.createErrs[index]; ok {
		return nil, err
	}
	f.seq++
	task := &model.Task{ID: fmt.Sprintf("child-%d", f.seq), Status: model.TaskStatusQueued}
	if status, ok := f.outcomes[index]; ok {
		task.Status = status
		if status == model.TaskStatusFailed {
			task.Error = "模拟生成失败"
		}
		if status == model.TaskStatusSucceeded {
			task.ResultJSON = fmt.Sprintf(`{"videoResourceId":"res-%s"}`, task.ID)
		}
	}
	if f.tasks == nil {
		f.tasks = map[string]*model.Task{}
	}
	f.tasks[task.ID] = task
	return task, nil
}

func (f *fakeBatchStore) ChildTask(id string) (*model.Task, error) {
	task, ok := f.tasks[id]
	if !ok {
		return nil, fmt.Errorf("task %s not found", id)
	}
	return task, nil
}

func (f *fakeBatchStore) UpdateBatchProgress(stage string, progress int) error {
	f.stages = append(f.stages, stage)
	f.progress = append(f.progress, progress)
	return nil
}

func batchTestInput(rows ...agentStoryboardShot) storyboardVideoBatchInput {
	return storyboardVideoBatchInput{ProjectID: "proj-1", Rows: rows, Width: 1280, Height: 720}
}

func TestRunStoryboardVideoBatchPartialFailure(t *testing.T) {
	store := &fakeBatchStore{
		createErrs: map[int]error{1: errors.New("同时排队或运行的任务最多 3 个")},
		outcomes:   map[int]model.TaskStatus{0: model.TaskStatusSucceeded, 2: model.TaskStatusSucceeded},
	}
	input := batchTestInput(
		agentStoryboardShot{Title: "镜一", Duration: 3, Motion: "推镜"},
		agentStoryboardShot{Title: "镜二", Duration: 4, Motion: "拉镜"},
		agentStoryboardShot{Title: "镜三", Duration: 5, Motion: "横移"},
	)
	input.FirstFrameResourceIDs = map[int]string{2: "res-frame-2"}

	results, err := runStoryboardVideoBatch(context.Background(), store, input)
	if err != nil {
		t.Fatalf("部分行失败不应阻断父任务：%v", err)
	}
	if store.seq != 2 {
		t.Fatalf("创建失败的行不应生成子任务，实际入队 %d 个", store.seq)
	}
	if results[1].Status != string(model.TaskStatusFailed) || results[1].Error == "" {
		t.Fatalf("第 2 行应记录创建失败，实际 %+v", results[1])
	}
	if results[0].Status != string(model.TaskStatusSucceeded) || results[0].VideoResourceID != "res-child-1" {
		t.Fatalf("第 1 行应成功并带资源 ID，实际 %+v", results[0])
	}
	if results[2].TaskID != "child-2" {
		t.Fatalf("第 3 行应挂到子任务 child-2，实际 %+v", results[2])
	}
	// 首帧资源按 shotNumber（下标+1）对齐；宽高与项目透传。
	if store.created[1].FirstFrameResourceID != "res-frame-2" {
		t.Fatalf("shotNumber=2 的首帧资源应透传给子任务，实际 %q", store.created[1].FirstFrameResourceID)
	}
	if store.created[0].FirstFrameResourceID != "" || store.created[0].Width != 1280 || store.created[0].Height != 720 {
		t.Fatalf("子任务请求参数错误：%+v", store.created[0])
	}
	// fan-out 后 1/3 行已终态（创建失败），收尾 3/3。
	if len(store.stages) < 2 || store.stages[0] != "已完成 1/3 行" || store.progress[0] != 35 {
		t.Fatalf("fan-out 后进度应为 1/3=35%%，实际 %v %v", store.stages, store.progress)
	}
	if store.stages[len(store.stages)-1] != "已完成 3/3 行" || store.progress[len(store.progress)-1] != 95 {
		t.Fatalf("收尾进度应为 3/3=95%%，实际 %v %v", store.stages, store.progress)
	}
}

func TestRunStoryboardVideoBatchChildFailsAfterRun(t *testing.T) {
	store := &fakeBatchStore{outcomes: map[int]model.TaskStatus{0: model.TaskStatusFailed, 1: model.TaskStatusSucceeded}}
	results, err := runStoryboardVideoBatch(context.Background(), store, batchTestInput(
		agentStoryboardShot{Title: "镜一", Duration: 3, Motion: "推镜"},
		agentStoryboardShot{Title: "镜二", Duration: 3, Motion: "拉镜"},
	))
	if err != nil {
		t.Fatalf("行失败不应让父任务失败：%v", err)
	}
	if results[0].Status != string(model.TaskStatusFailed) || results[0].Error != "模拟生成失败" {
		t.Fatalf("第 1 行应带子任务错误，实际 %+v", results[0])
	}
	if results[1].Status != string(model.TaskStatusSucceeded) {
		t.Fatalf("第 2 行应成功，实际 %+v", results[1])
	}
}

func TestRunStoryboardVideoBatchAllFailed(t *testing.T) {
	store := &fakeBatchStore{outcomes: map[int]model.TaskStatus{0: model.TaskStatusFailed, 1: model.TaskStatusFailed}}
	results, err := runStoryboardVideoBatch(context.Background(), store, batchTestInput(
		agentStoryboardShot{Title: "镜一", Duration: 3, Motion: "推镜"},
		agentStoryboardShot{Title: "镜二", Duration: 3, Motion: "拉镜"},
	))
	if err == nil || err.Error() != "全部 2 行生成失败，例如：模拟生成失败" {
		t.Fatalf("全败时父任务应失败，实际 %v", err)
	}
	if countStoryboardBatchSucceeded(results) != 0 {
		t.Fatalf("全败时不应有成功行：%+v", results)
	}
}

func TestPlanStoryboardMusicGroups(t *testing.T) {
	rows := []agentStoryboardShot{
		{Duration: 3, MusicGroupID: "seg-02", MusicMood: "紧张的追逐"},
		{Duration: 4, MusicGroupID: ""},
		{Duration: 8, MusicGroupID: "seg-02"},
	}
	plans := planStoryboardMusicGroups(rows, "复古港风夜色")
	if len(plans) != 2 {
		t.Fatalf("应分为 2 组，实际 %d：%+v", len(plans), plans)
	}
	if plans[0].MusicGroupID != "seg-02" || plans[1].MusicGroupID != "seg-01" {
		t.Fatalf("分组应按首次出现顺序（空组补 seg-01），实际 %+v", plans)
	}
	if plans[0].Prompt != "紧张的追逐，"+storyboardMusicPromptSuffix {
		t.Fatalf("有 mood 的组应以 mood 开头，实际 %q", plans[0].Prompt)
	}
	if plans[1].Prompt != "复古港风夜色，"+storyboardMusicPromptSuffix {
		t.Fatalf("mood 为空应回退项目风格描述，实际 %q", plans[1].Prompt)
	}
	if plans[0].DurationSeconds != 11 {
		t.Fatalf("组内时长应求和（3+8），实际 %d", plans[0].DurationSeconds)
	}
	clamped := planStoryboardMusicGroups([]agentStoryboardShot{{Duration: 700}}, "")
	if clamped[0].DurationSeconds != 600 {
		t.Fatalf("时长应 clamp 到 600，实际 %d", clamped[0].DurationSeconds)
	}
	tiny := planStoryboardMusicGroups([]agentStoryboardShot{{Duration: 0}}, "")
	if tiny[0].DurationSeconds != 10 {
		t.Fatalf("时长应 clamp 到 10，实际 %d", tiny[0].DurationSeconds)
	}
}

// recordingGateway 是 httptest stub Gateway 的记录器。
type recordingGateway struct {
	mu           sync.Mutex
	musicReqs    []map[string]any
	concatRaw    []string
	wavServerURL string
}

func (r *recordingGateway) recordMusic(body map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.musicReqs = append(r.musicReqs, body)
}

func (r *recordingGateway) recordConcat(raw string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.concatRaw = append(r.concatRaw, raw)
}

func TestRunStoryboardMusicBatchStubGateway(t *testing.T) {
	recorder := &recordingGateway{}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/music", func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("解析 /v1/music 请求失败：%v", err)
		}
		recorder.recordMusic(body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"url":%q,"duration_seconds":6}`, recorder.wavServerURL+"/music.wav")
	})
	mux.HandleFunc("/music.wav", func(w http.ResponseWriter, req *http.Request) {
		_, _ = w.Write([]byte("RIFF-fake-music"))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	recorder.wavServerURL = server.URL

	gw := &gatewayClient{baseURL: server.URL, httpClient: server.Client(), pollInterval: time.Millisecond, jobTimeout: 2 * time.Second}
	rows := []agentStoryboardShot{
		{Duration: 3, MusicGroupID: "seg-02", MusicMood: "紧张的追逐"},
		{Duration: 4},
		{Duration: 700, MusicGroupID: "seg-02"},
	}
	tracks, err := runStoryboardMusicBatch(context.Background(), gw, rows, "复古港风夜色")
	if err != nil {
		t.Fatalf("音乐批量不应失败：%v", err)
	}
	if len(recorder.musicReqs) != 2 {
		t.Fatalf("2 组应触发 2 次 /v1/music，实际 %d 次", len(recorder.musicReqs))
	}
	first := recorder.musicReqs[0]
	if first["prompt"] != "紧张的追逐，"+storyboardMusicPromptSuffix {
		t.Fatalf("第一段 prompt 错误，实际 %v", first["prompt"])
	}
	if first["duration_s"] != float64(600) {
		t.Fatalf("第一段时长应 clamp 到 600（3+700），实际 %v", first["duration_s"])
	}
	second := recorder.musicReqs[1]
	if second["prompt"] != "复古港风夜色，"+storyboardMusicPromptSuffix {
		t.Fatalf("mood 为空的组应回退项目风格描述，实际 %v", second["prompt"])
	}
	if second["duration_s"] != float64(10) {
		t.Fatalf("第二段时长应 clamp 到 10（4），实际 %v", second["duration_s"])
	}
	if len(tracks) != 2 || tracks[0].MusicGroupID != "seg-02" || tracks[1].MusicGroupID != "seg-01" {
		t.Fatalf("结果应与分组一致：%+v", tracks)
	}
	defer os.Remove(tracks[0].WavPath)
	if _, err := os.Stat(tracks[0].WavPath); err != nil {
		t.Fatalf("音乐 wav 应已下载到本地：%v", err)
	}
}

func TestStoryboardComposeMissingResources(t *testing.T) {
	rows := []agentStoryboardShot{{Duration: 3}, {Duration: 4}, {Duration: 5}}
	missing := storyboardComposeMissingVideos(rows, map[int]string{1: "r1", 3: "r3"})
	if len(missing) != 1 || missing[0] != 2 {
		t.Fatalf("镜头 2 缺视频资源，实际 %v", missing)
	}
	plans := planStoryboardMusicGroups([]agentStoryboardShot{{Duration: 3}, {Duration: 4, MusicGroupID: "seg-02"}}, "")
	if got := storyboardComposeMissingGroups(plans, map[string]string{"seg-01": "m1"}); len(got) != 1 || got[0] != "seg-02" {
		t.Fatalf("音乐段 seg-02 缺资源，实际 %v", got)
	}
}

func TestRunStoryboardComposeStubGateway(t *testing.T) {
	recorder := &recordingGateway{}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/concat", func(w http.ResponseWriter, req *http.Request) {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			t.Errorf("读取 /v1/concat 请求失败：%v", err)
		}
		recorder.recordConcat(string(raw))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"concat-1"}`))
	})
	mux.HandleFunc("/v1/videos/concat-1", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"concat-1","status":"completed"}`))
	})
	mux.HandleFunc("/v1/videos/concat-1/content", func(w http.ResponseWriter, req *http.Request) {
		_, _ = w.Write([]byte("fake-mp4-bytes"))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	gw := &gatewayClient{baseURL: server.URL, httpClient: server.Client(), pollInterval: time.Millisecond, jobTimeout: 2 * time.Second}
	shots := []composeShotInput{{ShotNumber: 3, Path: "/tmp/p3.mp4"}, {ShotNumber: 1, Path: "/tmp/p1.mp4"}, {ShotNumber: 2, Path: "/tmp/p2.mp4"}}
	segments := []concatMusicSegment{{Path: "/tmp/m1.wav", DurationS: 10}, {Path: "/tmp/m2.wav", DurationS: 20}}
	outputPath, err := runStoryboardCompose(context.Background(), gw, shots, segments, -3.5)
	if err != nil {
		t.Fatalf("合成不应失败：%v", err)
	}
	defer os.Remove(outputPath)
	if len(recorder.concatRaw) != 1 {
		t.Fatalf("应提交一次 /v1/concat，实际 %d 次", len(recorder.concatRaw))
	}
	var got concatJobRequest
	if err := json.Unmarshal([]byte(recorder.concatRaw[0]), &got); err != nil {
		t.Fatalf("解析 concat 请求失败：%v", err)
	}
	want := concatJobRequest{
		Shots:         []string{"/tmp/p1.mp4", "/tmp/p2.mp4", "/tmp/p3.mp4"},
		MusicSegments: segments,
		BgmGainDB:     -3.5,
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("concat 参数应按 shotNumber 排序且段对齐，实际 %s 期望 %s", gotJSON, wantJSON)
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("成片应已下载到本地：%v", err)
	}
}
