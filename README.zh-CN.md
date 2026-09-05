# 影策 · MediaGateway_YingCe(本地 GPU 渲染版)

[English](README.md) · 上游原始说明:[README.upstream.zh-CN.md](README.upstream.zh-CN.md)

本仓库是 [ddcat-ai/open-ai-canvas](https://github.com/ddcat-ai/open-ai-canvas)(影策)的个人 fork,
扩展为通过 [AI Media Gateway](https://github.com/vincent4reason/MediaGateway) 驱动
**Mac M5 Pro 48GB 全本地 GPU 媒体流水线**——生成不走云端 API,零按次成本。

## 相对上游新增

| 领域 | 改动 |
|---|---|
| **本地媒体推理** | 新渠道 `MediaGateway-Local`(OpenAI-Videos `newapi` 协议 + OpenAI 图像/音频/chat 协议面),生成全部路由到本地 GPU 网关:MiniMax-H3 视频(Metal)、iris.c FLUX.2 生图、CosyVoice + Qwen3-TTS 配音、ACE-Step 配乐 |
| **分镜渲染链** | `POST /projects/:id/shots/:shotId/render` 与 `/shots/render-all`:单镜流水 image → voice(TTS)→ video → music → 混音。台词 wav 作为 **Ref2VA 条件驱动口型**,h3 自渲染音轨**静音丢弃**,铺 TTS 原声 + BGM 垫底。两段式:快速**草稿**(512×288)→ 确认后完整**成片** |
| **首帧连续性** | render-all 用上一镜尾帧链接下一镜保证多镜一致性;缺尾帧显式报错而非静默断裂;完成后拼接成片并登记素材库 |
| **上游取消** | `newapi` 协议适配器补齐取消(取消请求 + 对账确认),画布里点「停止」会真正停掉本地 GPU 任务 |
| **声明式音频插件** | `mediagateway-audio` `.yingce-plugin`,把网关 `/v1/audio/speech` 封装成影策音频渠道模型 |
| **短剧技能链** | 本地适配的 Codex 技能:小说分析 → 改编 → 剧本 → 资产图 → 分镜 → 视频提示词,接入画布 GenerationTask |

其余能力(画布、角色、场景、分镜、3D 导演台、任务中心、渠道/模型管理、MCP)与上游一致,
完整功能清单见 [README.upstream.zh-CN.md](README.upstream.zh-CN.md)。

## 架构

```text
影策画布(本仓库,Go :8090 + React :3000)
   │  生成任务经渠道 "MediaGateway-Local"
   ▼
AI Media Gateway(:8600,Python)     github.com/vincent4reason/MediaGateway
   │  内存预算调度(40GB)、worker 生命周期
   ├── iris.c        生图   (FLUX.2 Klein,Metal)
   ├── h3.c          视频   (MiniMax-H3,Ref2VA 口型对齐,draft/quality 双档)
   ├── CosyVoice /   音频   (音色克隆 C001/C002 / Qwen3-TTS)
   │   Qwen3-TTS
   └── ACE-Step      配乐
```

台词口型:Voice Worker 先出台词 wav → 作为参考音频喂给 h3(Ref2VA)驱动口型动画 →
混音时静音 h3 渲染音轨、铺 TTS 原声,配乐由 Music Worker 循环垫底。

## 本地运行

依赖 [MediaGateway](https://github.com/vincent4reason/MediaGateway) 服务运行在 `127.0.0.1:8600`。

```bash
# 后端(Go,源码直跑,无 docker)
cd backend
CANVAS_BACKEND_ADDR=127.0.0.1:8090 CANVAS_DESKTOP_LOCAL_CHANNELS_ENABLED=true \
  go run ./cmd/server

# 前端
cd web
bun install
VITE_API_PROXY_TARGET=http://127.0.0.1:8090 bun run dev   # → http://localhost:3000
```

注意事项:

- 包管理只用 **bun**——`pnpm install` 会破坏 tiptap 依赖图(画布页双实例崩溃)
- 首个注册用户自动成为管理员
- 管理后台 → 渠道 → `MediaGateway-Local`:视频 `sora-2`、生图 `iris-image`、
  音频 `C001`/`C002`/`qwen3-tts`、文本 `qwen3.8-27b`
- 小说导入在 项目 → 章节 → 「导入小说」(.txt/.md,自动切章)

## 上游

本 fork 跟踪 [ddcat-ai/open-ai-canvas](https://github.com/ddcat-ai/open-ai-canvas)。
本地提交集中在分镜渲染 service/handler、`newapi` 协议适配器(取消)与制作工作台 UI;
同步上游时这些文件可能需要重放适配。
