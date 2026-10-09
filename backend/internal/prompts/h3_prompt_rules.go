package prompts

// h3VideoPromptRules 注入分鏰行受保護契約（StoryboardExecutionContract）的 H3 視頻提示詞
// 格式規則。權威依據：同目錄 base-en.txt（h3-prompt-writing skill，2026-10-10 快照），
// 這裡只取 videoPrompt 相關骨架：構圖/機位/光影/聲音四欄、畫外音措辭、無配樂寫 N/A。
//
// ⚠️ 注入有回歸前科：緊湊 JSON 指令曾誘發 27B 掉 durationSeconds（7f31628a 回滾）。
// ⚠️ G1 實驗（/tmp/h3gate/report.md，2026-10-10）：「只補骨架」類措辭會讓模型退化成
// 空骨架輸出（Run 1 FAIL），必須明確要求「完整文本、不是骨架不是摘要、細節照常寫足」
// （Run 2 PASS 關鍵句）。本塊措辭已按此校準，禁止改回「只補/僅輸出骨架」類表述。
// 任何改動後必須跑 G2 實測（20 鏡溫和劇本 e2e，驗 gate 通過率不掉、videoPrompt 呈
// 四欄完整文本）再上線。
const h3VideoPromptRules = `【videoPrompt 的 H3 格式規則——只約束以下字段的內容寫法，不改變上方 JSON 結構、字段名和全部必填字段】
每個 videoPrompt 都是完整可渲染的英文文本（不是欄位模板、不是骨架、不是摘要），內含構圖/機位/光影/聲音四欄結構，本鏡頭的動作、表情與內容細節照常寫足；每個 videoPrompt 只描述本鏡頭這一個鏡頭，按 H3 三段格式：
integrated_multimodal_description: [Shot 1] 開頭寫英文風格詞（跟隨當前項目畫風，如 Live-action、2D-animated、3D CG，不得默認改成真人攝影），隨後完整覆蓋四欄：構圖（景別、主體左右位置、視線、前中後景與視覺焦點）、機位（只保留一個主運鏡，寫成自然英文動作句，使用固定詞彙 Push In/Pull Out、Pan Left/Pan Right、Truck Left/Truck Right、Tilt Up/Tilt Down、Zoom In/Zoom Out、Arc Shot、Tracking Shot、Static Shot，需要時加 with small/large amplitude、at slow/fast speed）、光影（光源方向、質感與氛圍）、聲音（台詞與畫面內音效）。
- 台詞：說話者首次出現時在 <d> 外寫身份與音色並給穩定編號，如 The young woman with a quiet, breathy voice (S1) says: <d>[Chinese] 台詞原文逐字保留，不翻譯不改寫</d>；語言標籤按台詞實際語言。
- 畫外音固定寫 says in an off-screen voiceover: <d>…</d>，緊接一句 while his lips remain completely closed（指代與說話者一致）。
- overall_soundscape: 1–4 句英文，只寫環境聲、動作聲與非語言人聲，不重複台詞；劇本明確要求全程靜音時寫 N/A。
- non_diegetic_music: 1–3 句英文，只寫配樂樂器、速度與強弱變化；本鏡頭沒有配樂時必須寫 N/A。`
