# 控制面候選評估與功能借鑑決策（2026-10-10）

背景：評估「放棄影策、遷移到現成平台」的三個候選。結論先行：**三個都不遷移**；
唯一批准的借鑑 = Toonflow 3D 導演台核心移植（方案見 `docs/director3d_port_plan.md`）。
影策的護城河 = 本地無審查棧（h3/qwen_image/qwen uncensored）+ 粵語 TTS 雙引擎 + ref-audio 口型鏈，
**沒有任何上游會為這條路線服務**——換控制面解決不了這個根本矛盾。

## 三候選對比（均讀源碼驗證，非 README 轉述）

| | Toonflow | MoneyPrinterTurbo | huobao-drama |
|---|---|---|---|
| 領域匹配 | 中（短劇+漫劇畫布） | ❌ 素材混剪（Pexels/知識片） | **高**（小說→劇本→分鏡→角色→視頻→成片） |
| 技術棧 | Vue3+Bun+Drizzle | Python3.11+uv | Nuxt3+Hono+Mastra+better-sqlite3 |
| 模型接入 | **硬編碼雲端**（apiMart/compshare/metaso/tfRouter），無通用 OpenAI 面 | 文本/生圖 OpenAI 兼容可自定義 ✅；視頻全雲端私有協議 | 文本/生圖 OpenAI 兼容 ✅；視頻 3 adapter（火山/MiniMax/阿里），**baseUrl 可配** |
| TTS/口型 | 無獨立 provider | 無口型概念 | **完全沒有 TTS 環節**（數據模型有 refAudios 但無語音生成步驟） |
| 授權 | MIT | 寬鬆 | ⚠️ **CC BY-NC-SA 4.0**（非商業+同方式共享，與公開倉庫模式衝突） |
| 活躍度 | 高（130天800+ commits） | 高（1111 commits） | 中高（415 commits，v4.0.5 桌面端） |

## 各自結論

### Toonflow — 不遷移；**批准移植 3D 導演台核心**（唯一落地的借鑑）
- provider 硬編碼雲端，接 MediaGateway 要重寫接入層
- 但 `director3dNode` 核心價值真實：場景=zod JSON 文檔、AI 只發路徑補丁（治影策
  `compileDirectorPrompt` lossy 轉文字→站位漂移的根因病）、內置程序化人偶
- 移植方案、決策記錄、階段劃分：`docs/director3d_port_plan.md`

### MoneyPrinterTurbo — 不遷移也不借鑑
- 產品類別錯位：主題→素材庫混剪→字幕→成片，**無角色/分鏡/連續性/口型概念**
- 其視頻全雲端（秘塔 H3/Seedance 等，付費+過審）——本地無審查紅線直接出局
- 若哪天做營銷號/知識片：文本+生圖指 MediaGateway 約一天可通，屆時再說

### huobao-drama — 不遷移；記錄一個模式資產
- **MiniMax v2 協議 ≈ 本地 h3.c Ref2VA 契約**（first/last_frame、ref_image≤9、
  ref_video≤3、**ref_audio≤3**、混合≤12；baseUrl 可配）——給 MediaGateway 加
  MiniMax-v2 兼容面（1-2 天：create/query→job 隊列、URL 引用→本地檔案、SSRF 交互），
  huobao 即可用雲端協議驅動本地無審查 h3，**口型鏈也通**
- 但：①TTS 環節不存在（對白劇命門，要自己在 NC fork 裡造 2-4 天）②CC BY-NC-SA
  授權禁止抄代碼進本倉（SA 傳染）③分鏡契約/資產綁定/批次修復影策均更成熟
- **模式資產**：「雲端協議 shim 驅動本地引擎」——未來要給任何會說雲端 H3 話的
  客戶端提供本地接口，這是現成路線（接口要點見上）

## 可抄功能塊評估匯總

| 功能塊 | 影策對應 | 判定 |
|---|---|---|
| 3D 導演台+AI 擺人偶/運鏡（Toonflow） | 導演台手動擺、無 AI | ✅ **移植**（唯一批准，見 port_plan） |
| 小說→劇本分集（huobao script_rewriter） | short-drama-novel-analyze/write skills + canvas_text | ❌ 已覆蓋 |
| 分鏡 breaker（huobao） | 分鏡行全鏈（契約 gate+資產綁定）更成熟 | ❌ |
| 角色參考注入視頻 prompt（huobao @character） | 資產自動綁定+refs 組裝 | ❌ |
| 批次+失敗重試（huobao） | 批次+retryFailedBatchItems（2026-10-02 剛修過濾 bug） | ❌ |
| 視頻 prompt 工藝 SKILL.md（huobao 四語言） | h3 prompt 契約 | ⚠️ 只讀借鑑思路，**不抄文本**（SA） |
| 邊做邊學：雲端協議 shim（huobao MiniMax adapter） | — | ✅ 思路資產，記錄備用 |

## 附：同期已歸檔

- SDXL 全面退役清理清單：MediaGateway `docs/sdxl_cleanup_plan.md`（含 aux 四件套歸屬結論與恢復 recipe）
