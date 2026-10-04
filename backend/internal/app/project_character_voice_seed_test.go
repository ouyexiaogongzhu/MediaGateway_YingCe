package app

import (
	"testing"
	"time"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// 网关 Qwen3-TTS 接受的全部预置说话人，硬编码在测试里当护栏：
// 拿到模型 config.json 的 talker_config.spk_id 逐条抄下来的。
// app/model 那边少一个、多一个、拼错一个，这个测试就红。
var gatewayQwenSpeakers = map[string]bool{
	"vivian": true, "serena": true, "uncle_fu": true, "ryan": true,
	"aiden": true, "ono_anna": true, "sohee": true, "eric": true, "dylan": true,
}

// 影策 seed 出去的每个 voiceKey 都必须是网关认识的说话人，否则配音到网关那一步就报
// "Speaker not supported"，而用户看到的是「没配音」。
func TestBuiltinVoiceProfilesAreGatewayQwenSpeakers(t *testing.T) {
	profiles := builtinVoiceProfiles("user-seed")
	if len(profiles) != len(model.QwenVoiceKeys) {
		t.Fatalf("seeded %d profiles, want %d", len(profiles), len(model.QwenVoiceKeys))
	}
	if len(profiles) != len(gatewayQwenSpeakers) {
		t.Fatalf("seeded %d profiles but gateway has %d speakers", len(profiles), len(gatewayQwenSpeakers))
	}
	for _, profile := range profiles {
		if !gatewayQwenSpeakers[profile.VoiceKey] {
			t.Errorf("voiceKey %q is not a gateway Qwen3-TTS speaker", profile.VoiceKey)
		}
		if profile.Name != model.QwenVoiceDisplayName(profile.VoiceKey) || profile.Name == "" {
			t.Errorf("profile %q has unusable display name %q", profile.VoiceKey, profile.Name)
		}
		if profile.Status != "active" || profile.UserID != "user-seed" {
			t.Errorf("profile %q not seeded active for the user: %+v", profile.VoiceKey, profile)
		}
	}
}

// 端到端过一遍 voice_profiles → character_voice_bindings → shotVoiceKey：
// 这是 P0-5 的断链点，seed 修好之前这里恒返回空串、分镜永远静默跳过配音。
func TestShotVoiceKeyResolvesSeededQwenBinding(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+newID()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.VoiceProfile{}, &model.CharacterVoiceBinding{}, &model.Asset{}, &model.AssetVersion{}); err != nil {
		t.Fatal(err)
	}
	service := &Service{repo: repository.New(db)}
	seeded, err := service.ListVoiceProfiles("user-voice")
	if err != nil {
		t.Fatal(err)
	}
	if len(seeded) != len(gatewayQwenSpeakers) {
		t.Fatalf("ListVoiceProfiles returned %d profiles, want %d", len(seeded), len(gatewayQwenSpeakers))
	}

	var wanted model.VoiceProfile
	for _, profile := range seeded {
		if profile.VoiceKey == "serena" {
			wanted = model.VoiceProfile{ID: profile.ID, UserID: "user-voice", VoiceKey: profile.VoiceKey}
		}
	}
	if wanted.ID == "" {
		t.Fatal("serena profile not seeded")
	}
	now := time.Now()
	if err := db.Create(&model.Asset{ID: "char-1", UserID: "user-voice", Kind: "entity", Category: model.AssetCategoryCharacter, Status: model.AssetVersionStatusConfirmed, PrimaryVersionID: "char-1-v1", Title: "少女", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AssetVersion{ID: "char-1-v1", AssetID: "char-1", Version: 1, Status: model.AssetVersionStatusConfirmed, DefinitionJSON: "{}", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.CharacterVoiceBinding{ID: "binding-1", AssetVersionID: "char-1-v1", VoiceProfileID: wanted.ID, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}

	key := service.shotVoiceKey("user-voice", []model.ShotAssetReference{{AssetVersionID: "char-1-v1"}})
	if key != "serena" {
		t.Fatalf("shotVoiceKey = %q, want %q", key, "serena")
	}
	if !gatewayQwenSpeakers[key] {
		t.Fatalf("shotVoiceKey %q is not accepted by the gateway voice stage", key)
	}
	if other := service.shotVoiceKey("other-user", []model.ShotAssetReference{{AssetVersionID: "char-1-v1"}}); other != "" {
		t.Fatalf("shotVoiceKey leaked another user's voice: %q", other)
	}
}

// 一键视频的行只带角色资产 ID，版本号必须由 serviceBatchStore 回填，
// 否则 storyboardRowVoiceKey 全程返回空串——批量路径永远用渠道默认音色。
func TestResolveRowCharacterVersionsFillsVersionFromAssetID(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+newID()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.Project{}, &model.Asset{}, &model.AssetVersion{}, &model.ProjectAssetLink{},
		&model.VoiceProfile{}, &model.CharacterVoiceBinding{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := db.Create(&model.Project{ID: "proj-1", UserID: "user-row", Name: "批量音色", Status: model.ProjectStatusActive, Revision: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	// 主版本故意留空：只有 confirmed 版本可解析时也要能回填。
	if err := db.Create(&model.Asset{ID: "char-row", UserID: "user-row", Kind: "entity", Category: model.AssetCategoryCharacter, Status: model.AssetVersionStatusConfirmed, Title: "少女", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AssetVersion{ID: "char-row-v2", AssetID: "char-row", Version: 2, Status: model.AssetVersionStatusConfirmed, DefinitionJSON: "{}", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ProjectAssetLink{ID: "link-row", ProjectID: "proj-1", AssetID: "char-row", CreatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}

	service := &Service{repo: repository.New(db)}
	seeded, err := service.ListVoiceProfiles("user-row")
	if err != nil {
		t.Fatal(err)
	}
	var vivianID string
	for _, profile := range seeded {
		if profile.VoiceKey == "vivian" {
			vivianID = profile.ID
		}
	}
	row := batchShotToRowVideoRow(agentStoryboardShot{Title: "镜1", Dialogue: "你终于来了", CharacterIDs: []string{"char-row"}})
	if row.Characters[0].CharacterVersionID != "" {
		t.Fatalf("pure mapper should not resolve versions, got %q", row.Characters[0].CharacterVersionID)
	}
	service.resolveRowCharacterVersions("user-row", "proj-1", row)
	if got := row.Characters[0].CharacterVersionID; got != "char-row-v2" {
		t.Fatalf("CharacterVersionID = %q, want %q", got, "char-row-v2")
	}

	var voiceKey string
	if err := db.Create(&model.CharacterVoiceBinding{ID: "binding-row", AssetVersionID: "char-row-v2", VoiceProfileID: vivianID, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	voiceKey = service.storyboardRowVoiceKey("user-row", row.Characters)
	if voiceKey != "vivian" {
		t.Fatalf("storyboardRowVoiceKey = %q, want %q", voiceKey, "vivian")
	}
}