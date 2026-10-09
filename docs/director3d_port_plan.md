# 3D 導演台升級方案：移植 Toonflow director3dNode 核心（待啟動）

日期：2026-10-10 ｜ 來源：HBAI-Ltd/Toonflow-app（MIT）`packages/nodes/director3dNode` ｜ 狀態：**方案定稿，未動工**

## 目標與已拍板的決策

把 Toonflow 導演台的 AI 能力（文字→擺人偶/編排運鏡）嫁接進影策現有導演台，
**白模關鍵幀定位為構圖/運鏡參考**（配角色定妝照走 h3 Ref2VA/FL2VA——不做 AI 動作重定向到 GLB rig）。

| 決策點 | 拍板 |
|---|---|
| 預演關鍵幀用途 | 構圖/運鏡參考；P5（重定向 GLB rig）不做 |
| Director AI 渠道 | grok-chat-fast 先跑通，再試本地 qwen3.8 |
| 現有導演台狀態 | 基本沒用/半殘 → **動工第一步先驗證舊工作台在當前畫布能跑**（半天），再談加功能 |
| 動工順序 | 待定（P1+P2 先行 vs 一次到 P3）；SDXL 清理見 MediaGateway `docs/sdxl_cleanup_plan.md` |

## Toonflow 核心資產盤點（7 檔全讀，共 39KB 無框架代碼）

| 檔案 | KB | 職責 | 移植難度 |
|---|---|---|---|
| mannequin.ts | 4.9 | 程序化 16 關節人偶（原始 BufferGeometry，無蒙皮/GLB/CDN） | 複製即用 |
| motion.ts | 2.4 | 運鏡文檔 schema（≤120 幀、linear/smooth/cut）+ 四元數 slerp 採樣 | 幾乎原樣 |
| sceneAnimation.ts | 5.0 | 動作文檔 schema（軌道=objectId+joint，≤200 幀）+ AnimationMixer | 幾乎原樣 |
| scene.ts | 12.7 | 場景文檔 zod schema（9 幾何體+mannequin，嚴格邊界） | 中（去 threejson） |
| agentTools.ts | 9.4 | AI 編輯核心：readDocument/editDocument 路徑補丁、原子批次、zod 糾錯循環 | 中（換接口類型） |
| agentPrompt.ts | 3.9 | 系統提示詞（關節層級/約定/優先級/{{ref N}}） | 原樣（MIT） |
| renderMedia.ts | 5.2 | 離屏關鍵幀 PNG / MediaRecorder MP4 | **不搬**（影策 exportClayVideo 同款） |

Vue UI 層 69KB（60%）**不搬**——@vue-flow/core + element-plus 綁定，React 下重寫不值。
核心架構：**場景 = zod 校驗的 JSON 文檔；AI 不碰 three.js，只發路徑補丁**——正好治
`compileDirectorPrompt` 把場景 lossy 轉文字導致站位漂移的根因病。

依賴處置：zod（唯一新增依賴，Toonflow 用 v4 專有 API 共 3 處各一行改寫）；
threejson 0.1.0-alpha.9 **不需要**（影策 `director-scene.ts` 319 行運行時頂位）。

## 與影策現狀的咬合

| 影策已有 | 對應 | 判斷 |
|---|---|---|
| types/director.ts 20 骨 rig、四元數軌道 | 16 關節人偶、歐拉文檔 | 互補：mannequin 作新 actor 類型 |
| director-scene.ts 運行時 | threejson | 影策替身 |
| director-viewport.tsx 播放 | 採樣函數 | 掛進現有 viewport |
| exportClayVideo | renderVideo | 已有等價物 |
| onApply→參考圖→h3 FL2VA | 關鍵幀導出 | **閉環已通，只差 AI 擺動作** |
| chat 渠道（grok/qwen3.8） | ai.generate() | 換接口 |

## 階段

- **P0（0.5 天）**：驗證舊導演台在當前畫布可跑（狀態=半殘）
- **P1（1 天）**：mannequin.ts 原樣移植 + zod 文檔層（`director-document.ts`）+ mannequin 作新
  DirectorObject kind 接 placement/picking。驗收：擺 3 人偶+幾何體，保存/恢復 round-trip
- **P2（1 天）**：motion/sceneAnimation 移植（歐拉→四元數保留其寫法）+ applyPlan 映射到影策運行時。
  驗收：手寫 plan JSON 預演「走兩步+推鏡」
- **P3（2-3 天，核心）**：agentTools 移植，NodeAiTool→影策 chat tool_calls；agentPrompt 原文；
  `{{ref N}}` 接角色定妝照（AI 對著照片擺人偶）；40 次熔斷/10 分鐘/500KB 上限照抄。
  驗收：「女人偶坐床邊，鏡頭從門口緩推到中景」→ 合法 plan → 預演可看
- **P4（0.5-1 天）**：關鍵幀 PNG→現有 onApply→h3 FL2VA；運鏡文字併入 h3 prompt 編譯。
  驗收：一句話→預演→關鍵幀→h3 出片全鏈
- **P5**：不做（見決策表）

## 風險

| 風險 | 對策 |
|---|---|
| 本地 qwen 工具調用弱 | zod 糾錯循環（Toonflow 內建）+ grok 先行 + 40 次熔斷 |
| zod v4 API 差異 | 裝最新 zod；3 處一行改寫 |
| 文檔與場景漂移 | AI 只走文檔路徑；手動擺姿落盤時同步進文檔 |

**不做**：Vue UI 層、Sky/光照面板、MP4 導出、多方案 history（先單方案）。

合計 4-6 天（含 P0）。P1+P2 與 P3 分 PR，後者可獨立回退。
