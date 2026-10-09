package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestStoryboardRowsModelConfig(t *testing.T) {
	channel := storyboardRowsModelConfig("ch1::gpt-x", "")
	if !reflect.DeepEqual(channel, map[string]any{"channelId": "ch1", "model": "gpt-x"}) {
		t.Fatalf("系统渠道模型应拆成 channelId+model，得到 %v", channel)
	}
	if managed := storyboardRowsModelConfig("ch1::gpt-x", "lm-1"); len(managed) != 0 {
		t.Fatalf("前台模型模式 config 不能带 channelId，得到 %v", managed)
	}
	if bare := storyboardRowsModelConfig("bare-model", ""); len(bare) != 0 {
		t.Fatalf("无渠道编码应留空交给 admission 报错，得到 %v", bare)
	}
	if !strings.Contains(StoryboardContractInstruction, "shots") {
		t.Fatalf("契约指令缺少 shots 输出要求")
	}
}

func TestShouldRetryStoryboardOutput(t *testing.T) {
	oneShot := `{"shots":[{"timeRange":"0:00–0:05","action":"开场"}]}`
	cases := []struct {
		name  string
		text  string
		retry bool
	}{
		{"纯散文", "我針對您的優化版進行了微調與細化，劇本如下……", true},
		{"空输出", "", true},
		{"shots 对象", oneShot, false},
		{"durationSeconds 行（schema 硬約束產出）", `{"shots":[{"durationSeconds":5,"action":"开场"}]}`, false},
		{"混合 duration/timeRange", `[{"durationSeconds":6},{"timeRange":"0:05–0:11"}]`, false},
		{"行缺 durationSeconds 与 timeRange（宿主可推导，不重试）", `{"shots":[{"action":"开场"}]}`, false},
		{"行缺 shotNumber 与 dialogue（宿主补齐，不重试）", `{"shots":[{"timeRange":"0:00–0:05"}]}`, false},
		{"散文包裹 shots", "好的，以下是分镜：\n" + oneShot + "\n如需調整請告知。", false},
		{"rows 键", `{"rows":[{"timeRange":"0:00–0:05"}]}`, false},
		{"裸数组", `[{"timeRange":"0:00–0:05"}]`, false},
		{"空 shots", `{"shots":[]}`, true},
		{"无关 JSON", `{"note":"这是一段说明"}`, true},
		{"截断 JSON", `{"shots":[{"timeRange":"0:00`, true},
	}
	for _, tc := range cases {
		if got := shouldRetryStoryboardOutput(tc.text); got != tc.retry {
			t.Fatalf("%s: shouldRetryStoryboardOutput = %v, 期望 %v", tc.name, got, tc.retry)
		}
	}
	if !strings.Contains(storyboardRetrySuffix, "storyboard-plan/v3") || !strings.Contains(storyboardRetrySuffix, "第一個字符必須是 {") {
		t.Fatalf("重试后缀缺少关键格式要求：%s", storyboardRetrySuffix)
	}
}

func TestNormalizeStoryboardTaskText(t *testing.T) {
	result := map[string]interface{}{"mode": "text", "text": `{"title":"hotel","logline":"x","shots":[{"description":"A壓住B","videoPrompt":"緩推","visualPrompt":"床沿","shotType":"中景","characterIds":["A","B"],"durationSeconds":10,"sfxTags":["breath"],"assetRefs":[{"name":"床"},{"name":"吊燈"}]}]}`}
	normalizeStoryboardTaskText(result, result["text"].(string))
	var out struct {
		Title string           `json:"title"`
		Rows  []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal([]byte(result["text"].(string)), &out); err != nil {
		t.Fatalf("歸一產物不是合法 JSON: %v", err)
	}
	if out.Title != "hotel" || len(out.Rows) != 1 {
		t.Fatalf("title=%q rows=%d", out.Title, len(out.Rows))
	}
	row := out.Rows[0]
	for _, check := range [][3]string{
		{"plotDescription", "A壓住B", "description 別名"},
		{"videoMotionPrompt", "緩推", "videoPrompt 別名"},
		{"imageGenerationPrompt", "床沿", "visualPrompt 別名"},
		{"shotSize", "中景", "shotType 別名"},
		{"characters", "", "characterIds→characters"},
	} {
		value := row[check[0]]
		if check[1] == "" {
			if value == nil {
				t.Fatalf("%s 應存在（%s）", check[0], check[2])
			}
			continue
		}
		if value != check[1] {
			t.Fatalf("%s = %v, 期望 %v（%s）", check[0], value, check[1], check[2])
		}
	}
	if len(row["characters"].([]any)) != 2 {
		t.Fatalf("characters 綁定失敗: %v", row["characters"])
	}
	refs, ok := row["characters"].([]any)
	if !ok {
		t.Fatalf("characters 應為數組: %v", row["characters"])
	}
	for i, ref := range refs {
		refMap, isMap := ref.(map[string]any)
		if !isMap || refMap["characterName"] == "" {
			t.Fatalf("characters[%d] 應為 {characterName} 對象，得到 %v", i, ref)
		}
	}
	if _, ok := row["assetRefs"]; !ok {
		t.Fatal("assetRefs 透傳失敗")
	}
	if _, ok := row["sfxTags"]; !ok {
		t.Fatal("sfxTags 透傳失敗")
	}
}

// 跨鏰物理狀態連續性契約：指令必須要求以 continuityOut 交接為核對依據、持續填寫、
// 矛盾時修後不瞬移；含 continuityOut 的樣本回應仍須通過重試門與歸一解析。
func TestStoryboardContractContinuityInstruction(t *testing.T) {
	for _, phrase := range []string{"continuityOut", "服裝狀態、姿勢、位置與道具", "不得憑空新增道具或讓角色瞬移"} {
		if !strings.Contains(StoryboardContractInstruction, phrase) {
			t.Fatalf("契約指令缺少跨鏰連續性要求 %q", phrase)
		}
	}
	sample := `{"shots":[{"timeRange":"0:00–0:05","action":"主角舉杯","continuityOut":"杯停在唇邊"},{"timeRange":"0:05–0:12","action":"主角放下杯"}]}`
	if shouldRetryStoryboardOutput(sample) {
		t.Fatal("含 continuityOut 的樣本回應不應觸發重試")
	}
	result := map[string]interface{}{"text": sample}
	normalizeStoryboardTaskText(result, sample)
	var out struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal([]byte(result["text"].(string)), &out); err != nil {
		t.Fatalf("含 continuityOut 的樣本歸一後不是合法 JSON: %v", err)
	}
	if len(out.Rows) != 2 {
		t.Fatalf("應保留 2 行，得到 %d", len(out.Rows))
	}
}

// 宿主拥有结构：shotNumber 一律按保留行序由宿主赋值（模型自编号曾造成前端键位静默
// 重排，绝不读取）；durationSeconds 缺失或非正数时从 timeRange 推导，仍无则取项目
// 默认时长；空 dialogue 不是缺陷，行原样保留。
func TestNormalizeStoryboardTaskTextHostOwnsStructure(t *testing.T) {
	text := `{"shots":[` +
		`{"shotNumber":9,"description":"模型编号应被忽略","timeRange":"0:00–0:05"},` +
		`{"description":"无时长无区间"},` +
		`{"description":"负时长走区间推导","durationSeconds":-3,"timeRange":"0:20–0:31"},` +
		`{"description":"字符串时长","durationSeconds":"8"},` +
		`{"description":"空对白合法","dialogue":""}]}`
	result := map[string]interface{}{"text": text}
	normalizeStoryboardTaskText(result, text)
	var out struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal([]byte(result["text"].(string)), &out); err != nil {
		t.Fatalf("归一输出不是 JSON：%v", err)
	}
	if len(out.Rows) != 5 {
		t.Fatalf("应保留 5 行，得到 %d", len(out.Rows))
	}
	for i, row := range out.Rows {
		if row["shotNumber"] != float64(i+1) {
			t.Fatalf("第 %d 行 shotNumber = %v, 期望宿主行序 %d", i, row["shotNumber"], i+1)
		}
	}
	expectations := []struct {
		name     string
		duration float64
	}{
		{"timeRange 推导 5 秒", 5},
		{"无来源取默认 6 秒", storyboardRowDefaultDurationSeconds},
		{"非正数时长走 timeRange 推导 11 秒", 11},
		{"字符串时长归一为 8 秒", 8},
		{"无来源取默认 6 秒（第二行）", storyboardRowDefaultDurationSeconds},
	}
	for i, expectation := range expectations {
		got, ok := out.Rows[i]["durationSeconds"].(float64)
		if !ok || got != expectation.duration {
			t.Fatalf("%s：第 %d 行 durationSeconds = %v, 期望 %v", expectation.name, i, out.Rows[i]["durationSeconds"], expectation.duration)
		}
	}
	if dialogue, ok := out.Rows[4]["dialogue"]; !ok || dialogue != "" {
		t.Fatalf("空 dialogue 不应被视为缺陷，得到 %v", out.Rows[4]["dialogue"])
	}
}

// 部分解析保留：解析得出部分有效行时不整批丢弃（解析器保留有效行并统计 dropped），
// 全无效才整体拒绝——确认既有行为不回归。
func TestParseScriptStoryboardRowsKeepsPartialParses(t *testing.T) {
	valid := `{"shots":[` +
		`{"description":"有效行","durationSeconds":6},` +
		`{"description":"缺时长缺区间"},` +
		`{"description":"另一有效行","timeRange":"0:06–0:12"}]}`
	rows, dropped, err := parseScriptStoryboardRows(valid)
	if err != nil {
		t.Fatalf("部分有效行不应整体拒绝: %v", err)
	}
	if len(rows) != 2 || dropped != 1 {
		t.Fatalf("应保留 2 行丢弃 1 行，得到 %d 行 dropped=%d", len(rows), dropped)
	}
	_, _, err = parseScriptStoryboardRows(`{"shots":[{"description":"唯一行缺时长"}]}`)
	if err == nil {
		t.Fatal("零有效行应整体拒绝")
	}
}

// h3Formatted 标记：归一成功时行出生即 H3 格式，必须打标。
func TestNormalizeStoryboardTaskTextMarksH3Formatted(t *testing.T) {
	text := `{"shots":[{"title":"开场","durationSeconds":6,"videoPrompt":"integrated_multimodal_description: [Shot 1] ...","timeRange":"0:00–0:06"}]}`
	result := map[string]interface{}{"text": text}
	normalizeStoryboardTaskText(result, text)
	var out struct {
		H3Formatted bool             `json:"h3Formatted"`
		Rows        []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal([]byte(result["text"].(string)), &out); err != nil {
		t.Fatalf("归一输出不是 JSON：%v", err)
	}
	if !out.H3Formatted {
		t.Fatalf("归一成功时应打 h3Formatted=true")
	}
	if len(out.Rows) != 1 {
		t.Fatalf("应保留 1 行，得到 %d", len(out.Rows))
	}
}
