package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRowVideoGateway 记录编排全部 Gateway 交互，不打真网关。
type fakeRowVideoGateway struct {
	speechVoiceIDs []string
	speechDuration float64
	speechErr      error

	videoReqs []rowVideoJobRequest
	videoErr  error

	mixVideoJobIDs []string
	mixTracks      [][]rowVideoMixTrack
	mixErr         error

	waitCalls  []string
	waitErrors map[string]error
}

func (f *fakeRowVideoGateway) CreateVideoJob(ctx context.Context, req rowVideoJobRequest) (string, error) {
	if f.videoErr != nil {
		return "", f.videoErr
	}
	f.videoReqs = append(f.videoReqs, req)
	return fmt.Sprintf("video-job-%d", len(f.videoReqs)), nil
}

func (f *fakeRowVideoGateway) SynthesizeSpeech(ctx context.Context, voiceID string, text string, speed float64) (rowVideoSpeech, error) {
	if f.speechErr != nil {
		return rowVideoSpeech{}, f.speechErr
	}
	f.speechVoiceIDs = append(f.speechVoiceIDs, voiceID)
	if f.speechDuration <= 0 {
		f.speechDuration = 2.4
	}
	wav := filepath.Join(os.TempDir(), fmt.Sprintf("canvas-row-voice-test-%d.wav", len(f.speechVoiceIDs)))
	if err := os.WriteFile(wav, []byte("RIFF-fake"), 0o600); err != nil {
		return rowVideoSpeech{}, err
	}
	return rowVideoSpeech{WavPath: wav, DurationSeconds: f.speechDuration}, nil
}

func (f *fakeRowVideoGateway) CreateMixJob(ctx context.Context, videoJobID string, tracks []rowVideoMixTrack) (string, error) {
	if f.mixErr != nil {
		return "", f.mixErr
	}
	f.mixVideoJobIDs = append(f.mixVideoJobIDs, videoJobID)
	f.mixTracks = append(f.mixTracks, tracks)
	return fmt.Sprintf("mix-job-%d", len(f.mixVideoJobIDs)), nil
}

func (f *fakeRowVideoGateway) WaitJob(ctx context.Context, jobID string) (rowVideoJobResult, error) {
	f.waitCalls = append(f.waitCalls, jobID)
	if err, ok := f.waitErrors[jobID]; ok {
		return rowVideoJobResult{}, err
	}
	return rowVideoJobResult{OutputPath: "/tmp/" + jobID + ".mp4"}, nil
}

func rowVideoTestParams(row storyboardRowVideoRow) rowVideoParams {
	return rowVideoParams{Row: row, FirstFrameDataURL: "data:image/png;base64,QUJD", VoiceKey: "voice-001", Speed: 1.0}
}

func TestRunStoryboardRowVideo(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		row     storyboardRowVideoRow
		fake    fakeRowVideoGateway
		wantErr string
		verify  func(t *testing.T, fake *fakeRowVideoGateway, out rowVideoOutcome)
	}{
		{
			name: "对白行：tts 后带参考音轨生成视频，mix 只混两条 sfx",
			row: storyboardRowVideoRow{
				Dialogue: "你终于来了。", DurationSeconds: 3, VoiceMode: "dialogue",
				VideoMotionPrompt: "推镜", SfxTags: []string{"ambience_wind", "door_open"},
				Characters: []storyboardRowCharacterRef{{CharacterName: "张三", CharacterVersionID: "v1"}},
			},
			verify: func(t *testing.T, fake *fakeRowVideoGateway, out rowVideoOutcome) {
				if len(fake.speechVoiceIDs) != 1 || fake.speechVoiceIDs[0] != "voice-001" {
					t.Fatalf("TTS 应按绑定音色调用一次，实际 %v", fake.speechVoiceIDs)
				}
				if len(fake.videoReqs) != 1 {
					t.Fatalf("应提交一次视频任务")
				}
				video := fake.videoReqs[0]
				if len(video.ReferenceAudios) != 1 || !strings.HasPrefix(video.ReferenceAudios[0], "data:audio/wav;base64,") {
					t.Fatalf("对白行视频应带一条 wav data URL 参考音轨，实际 %v", video.ReferenceAudios)
				}
				// tts 2.4s → ceil 3；durationSeconds 3 → seconds 3
				if video.Seconds != 3 {
					t.Fatalf("seconds 应为 max(3, ceil(2.4))=3，实际 %d", video.Seconds)
				}
				if video.Size != "" {
					t.Fatalf("未传宽高时不应带 size，实际 %q", video.Size)
				}
				if len(video.ReferenceImages) != 1 || video.ReferenceImages[0] != "data:image/png;base64,QUJD" {
					t.Fatalf("首帧应作为 reference_images 传入，实际 %v", video.ReferenceImages)
				}
				if len(fake.mixTracks) != 1 || len(fake.mixTracks[0]) != 2 {
					t.Fatalf("对白行 mix 应只有两条 sfx 轨，实际 %v", fake.mixTracks)
				}
				if fake.mixTracks[0][0].SfxTag != "ambience_wind" || fake.mixTracks[0][0].GainDB != 0 || fake.mixTracks[0][0].StartS != 0 {
					t.Fatalf("sfx 轨参数错误：%v", fake.mixTracks[0][0])
				}
				if fake.mixVideoJobIDs[0] != out.VideoJobID {
					t.Fatalf("mix 应挂在视频 job 上")
				}
				if out.MixOutputPath != "/tmp/"+out.MixJobID+".mp4" {
					t.Fatalf("应返回混音输出路径，实际 %q", out.MixOutputPath)
				}
			},
		},
		{
			name: "旁白行：tts 不进视频，mix 叠 sfx 与旁白两轨",
			row: storyboardRowVideoRow{
				Dialogue: "夜色渐深。", DurationSeconds: 4, VoiceMode: "voiceover",
				VideoMotionPrompt: "拉镜", SfxTags: []string{"ambience_night"},
			},
			verify: func(t *testing.T, fake *fakeRowVideoGateway, out rowVideoOutcome) {
				if len(fake.videoReqs) != 1 || len(fake.videoReqs[0].ReferenceAudios) != 0 {
					t.Fatalf("旁白行视频不应带参考音轨")
				}
				if len(fake.mixTracks[0]) != 2 {
					t.Fatalf("旁白行 mix 应为 sfx+旁白两轨，实际 %v", fake.mixTracks[0])
				}
				last := fake.mixTracks[0][1]
				if last.SfxTag != "" || last.Path == "" || !strings.HasSuffix(last.Path, ".wav") {
					t.Fatalf("第二轨应为旁白 wav 路径，实际 %+v", last)
				}
			},
		},
		{
			name: "无台词行：跳过 tts，mix 只混 sfx",
			row: storyboardRowVideoRow{
				DurationSeconds: 5, VoiceMode: "dialogue",
				VideoMotionPrompt: "横移", SfxTags: []string{"thunder"},
			},
			verify: func(t *testing.T, fake *fakeRowVideoGateway, out rowVideoOutcome) {
				if len(fake.speechVoiceIDs) != 0 {
					t.Fatalf("无台词不应调用 TTS")
				}
				if len(fake.videoReqs[0].ReferenceAudios) != 0 {
					t.Fatalf("无配音不应带参考音轨")
				}
				if len(fake.mixTracks[0]) != 1 || fake.mixTracks[0][0].SfxTag != "thunder" {
					t.Fatalf("应只混一条 sfx，实际 %v", fake.mixTracks[0])
				}
				if out.Seconds != 5 {
					t.Fatalf("seconds 应为行时长 5，实际 %d", out.Seconds)
				}
			},
		},
		{
			name: "sfx 与声音全空：mix 用空 tracks（剥音轨）",
			row: storyboardRowVideoRow{
				DurationSeconds: 3, VoiceMode: "dialogue",
				VideoMotionPrompt: "固定机位", Characters: []storyboardRowCharacterRef{{CharacterName: "张三", CharacterVersionID: "v1"}},
			},
			verify: func(t *testing.T, fake *fakeRowVideoGateway, out rowVideoOutcome) {
				if len(fake.mixTracks[0]) != 0 {
					t.Fatalf("应提交空 tracks，实际 %v", fake.mixTracks[0])
				}
			},
		},
		{
			name: "存量行缺声音标注：走 fallback 补齐 sfx",
			row: storyboardRowVideoRow{
				DurationSeconds: 4, AudioEffects: "夜里风声呼啸",
				VideoMotionPrompt: "推镜", Characters: []storyboardRowCharacterRef{{CharacterName: "张三", CharacterVersionID: "v1"}},
			},
			verify: func(t *testing.T, fake *fakeRowVideoGateway, out rowVideoOutcome) {
				if out.VoiceMode != "dialogue" || len(out.SfxTags) != 2 || out.SfxTags[0] != "ambience_night" || out.SfxTags[1] != "ambience_wind" {
					t.Fatalf("fallback 应按出现顺序补齐 sfxTags，实际 %s %v", out.VoiceMode, out.SfxTags)
				}
				if len(fake.mixTracks[0]) != 2 || fake.mixTracks[0][0].SfxTag != "ambience_night" {
					t.Fatalf("mix 应包含 fallback 出的 sfx 轨，实际 %v", fake.mixTracks[0])
				}
			},
		},
		{
			name: "tts 时长超行时长：seconds 向上取整扩到配音长度",
			row: storyboardRowVideoRow{
				Dialogue: "很长的一句台词。", DurationSeconds: 3, VoiceMode: "voiceover",
				VideoMotionPrompt: "推镜",
			},
			fake: fakeRowVideoGateway{speechDuration: 7.2},
			verify: func(t *testing.T, fake *fakeRowVideoGateway, out rowVideoOutcome) {
				if out.Seconds != 8 {
					t.Fatalf("seconds 应为 ceil(7.2)=8，实际 %d", out.Seconds)
				}
			},
		},
		{
			name:    "tts 失败：错误传播且不提交视频/混音",
			row:     storyboardRowVideoRow{Dialogue: "台词", DurationSeconds: 3, VideoMotionPrompt: "推镜"},
			fake:    fakeRowVideoGateway{speechErr: errors.New("voice engine down")},
			wantErr: "TTS 生成失败：voice engine down",
			verify: func(t *testing.T, fake *fakeRowVideoGateway, out rowVideoOutcome) {
				if len(fake.videoReqs) != 0 || len(fake.mixTracks) != 0 {
					t.Fatalf("tts 失败后不应继续提交视频/混音")
				}
			},
		},
		{
			name:    "视频轮询失败：错误带 job id 且不提交混音",
			row:     storyboardRowVideoRow{Dialogue: "台词", DurationSeconds: 3, VideoMotionPrompt: "推镜"},
			fake:    fakeRowVideoGateway{waitErrors: map[string]error{"video-job-1": errors.New("gateway job video-job-1 failed: gpu oom")}},
			wantErr: "视频生成失败（job video-job-1）：gateway job video-job-1 failed: gpu oom",
			verify: func(t *testing.T, fake *fakeRowVideoGateway, out rowVideoOutcome) {
				if len(fake.mixTracks) != 0 {
					t.Fatalf("视频失败后不应提交混音")
				}
			},
		},
		{
			name:    "缺少视频提示词：快速失败",
			row:     storyboardRowVideoRow{Dialogue: "台词", DurationSeconds: 3},
			wantErr: "分镜行缺少 videoMotionPrompt，无法生成视频",
			verify: func(t *testing.T, fake *fakeRowVideoGateway, out rowVideoOutcome) {
				if len(fake.speechVoiceIDs) != 0 {
					t.Fatalf("校验应先于 TTS")
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := tt.fake
			out, err := runStoryboardRowVideo(ctx, &fake, rowVideoTestParams(tt.row))
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("期望错误 %q，实际 %v", tt.wantErr, err)
				}
			} else if err != nil {
				t.Fatalf("不应失败：%v", err)
			}
			tt.verify(t, &fake, out)
		})
	}
}
