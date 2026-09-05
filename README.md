# 影策 · MediaGateway_YingCe

A personal fork of [ddcat-ai/open-ai-canvas](https://github.com/ddcat-ai/open-ai-canvas)
(影策 — an AI film & drama creation workbench), extended to drive a **fully local
GPU media pipeline** on a Mac M5 Pro 48GB via [AI Media Gateway](https://github.com/vincent4reason/MediaGateway).

**中文文档:[README.zh-CN.md](README.zh-CN.md)** · 上游原始中文说明:[README.upstream.zh-CN.md](README.upstream.zh-CN.md)

## What this fork adds on top of upstream

| Area | Change |
|---|---|
| **Local media inference** | New admin channel `MediaGateway-Local` (OpenAI-Videos `newapi` protocol + OpenAI image/audio/chat faces) routes generation to a local GPU gateway — MiniMax-H3 video (Metal), iris.c FLUX.2 image, CosyVoice + Qwen3-TTS voices, ACE-Step music. No cloud API, no per-call cost. |
| **Shot render pipeline** | `POST /projects/:id/shots/:shotId/render` and `/shots/render-all`: per-shot chain image → voice (TTS) → video → music → mux. Dialogue WAV conditions the video model (**Ref2VA lip sync**), the model's own audio track is **muted** and the TTS original + BGM bed are mixed back. Two-stage: fast **draft** (512×288) then full **quality** render. |
| **First-frame continuity** | render-all chains shots through the previous shot's last frame for multi-shot consistency; a missing tail frame fails loudly instead of silently breaking continuity. Then concatenates and registers the final cut in the asset store. |
| **Upstream cancellation** | The `newapi` protocol adapter now implements cancellation (build-cancel + reconciliation), so cancelling a task in the canvas actually stops the local GPU job queue entry. |
| **Declarative audio plugin** | `mediagateway-audio` `.yingce-plugin` adds audio-capable channel models backed by the gateway's `/v1/audio/speech`. |
| **Short-drama skill chain** | Local-adapted Codex skills: novel-analyze → develop → write → image-prompts → storyboard → video-prompts, wired to the canvas GenerationTask flow. |

Everything else (canvas, characters, scenes, storyboards, 3D director stage,
task center, channels/models admin, MCP) follows upstream — see
[README.upstream.zh-CN.md](README.upstream.zh-CN.md) for the full feature tour.

## Architecture

```text
影策 canvas (this repo, Go :8090 + React :3000)
   │  generation tasks via channel "MediaGateway-Local"
   ▼
AI Media Gateway (:8600, Python)        github.com/vincent4reason/MediaGateway
   │  memory-budget scheduler (40GB), worker lifecycle
   ├── iris.c        image   (FLUX.2 Klein, Metal)
   ├── h3.c          video   (MiniMax-H3, Ref2VA lip sync, profiles draft/quality)
   ├── CosyVoice /   audio   (voice clones C001/C002 / Qwen3-TTS)
   │   Qwen3-TTS
   └── ACE-Step      music
```

Dialogue lip sync: the TTS wav is sent to h3.c as a reference-audio condition
(Ref2VA) so lips follow the voice; the model-rendered track is discarded and the
original TTS audio is laid back over the picture, with the music worker's BGM
looped underneath.

## Run locally

Requires the [MediaGateway](https://github.com/vincent4reason/MediaGateway) service on `127.0.0.1:8600`.

```bash
# backend (Go, no docker, run from source)
cd backend
CANVAS_BACKEND_ADDR=127.0.0.1:8090 CANVAS_DESKTOP_LOCAL_CHANNELS_ENABLED=true \
  go run ./cmd/server

# frontend
cd web
bun install
VITE_API_PROXY_TARGET=http://127.0.0.1:8090 bun run dev   # → http://localhost:3000
```

Notes:

- `bun` is the canonical package manager here — `pnpm install` breaks the tiptap
  dependency graph (dual-instance crash on canvas pages).
- First registered user becomes admin.
- Admin → Channels → `MediaGateway-Local`: video `sora-2`, image `iris-image`,
  audio `C001`/`C002`/`qwen3-tts`, text `qwen3.8-27b`.
- Novel import lives in project → Chapters → 「导入小说」(.txt/.md, auto chapter split).

## Upstream

This fork tracks [ddcat-ai/open-ai-canvas](https://github.com/ddcat-ai/open-ai-canvas).
Local commits touch the shot-render service/handlers, the `newapi` protocol
adapter (cancellation), and the production workbench UI; expect rebasing work
when syncing upstream.
