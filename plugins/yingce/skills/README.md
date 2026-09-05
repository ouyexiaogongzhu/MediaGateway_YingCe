# 影策技能库

插件安装后，Codex 会在新线程中自动发现 `skills/` 下的技能。技能不是一组需要用户手动粘贴的 prompt，而是按请求匹配、按需加载的工作流约束。

当前技能：

- `open-canvas`：打开并连接本地画布。
- `canvas`：通用画布操作路由。
- `canvas-context`：读取语义上下文和资源状态。
- `canvas-editing`：可靠写入、批量校验和结果复核。
- `asset-aware-generation`：复用角色/场景/道具/风格资源进行生成。

短剧创意链（源自 drama-skills，产物落本地工作区，摘要与提示词回流画布节点，生成走影策共享 GenerationTask）：

- `short-drama-novel-analyze`：长篇小说拆解为可追溯的原著分析与分集候选。
- `short-drama-develop`：改编契约、故事引擎与分集地图。
- `short-drama-write`：单集卡、因果节拍与可拍摄 Markdown 剧本。
- `short-drama-image-prompts`：资产参考图通用提示词（角色/场景/道具/风格帧）。
- `short-drama-storyboard`：原文落实表、镜头设计与冻结关键帧提示词。
- `short-drama-video-prompts`：逐镜运动规格、视频提示词与时间线音乐规格。

安装或更新技能后，建议新建 Codex 线程，让技能和 MCP 工具重新加载。
