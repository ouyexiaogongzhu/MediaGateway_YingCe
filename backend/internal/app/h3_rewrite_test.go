package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"
)

// newH3RewriteTestService 空库 Service：渠道/模型表为空 ⇒ 配置发现必然降级，不触网。
func newH3RewriteTestService(t *testing.T) *Service {
	t.Helper()
	db := newSQLiteTestDB(t)
	if err := db.AutoMigrate(&model.ModelChannel{}, &model.ChannelModel{}); err != nil {
		t.Fatal(err)
	}
	return &Service{repo: repository.New(db)}
}

func TestH3ExtractProtectedSpans(t *testing.T) {
	prompt := `女人坐在床沿 <d voice="vivian">嗯…</d> 身体前倾 @图片1 特写 @图片2 全景 <d voice="vivian">不要停</d> 结尾 @图片12`
	spans := h3ExtractProtectedSpans(prompt, h3ProtectedSpanPatterns)
	if len(spans) != 5 {
		t.Fatalf("expected 5 spans, got %d: %q", len(spans), spans)
	}
	if spans[0] != `<d voice="vivian">嗯…</d>` || spans[1] != `<d voice="vivian">不要停</d>` || spans[2] != "@图片1" || spans[3] != "@图片2" || spans[4] != "@图片12" {
		t.Fatalf("unexpected spans: %q", spans)
	}
	// 无 span 的 prompt 恒通过。
	if got := h3ExtractProtectedSpans("no spans here", h3ProtectedSpanPatterns); len(got) != 0 {
		t.Fatalf("expected no spans, got %q", got)
	}
}

func TestH3VerifySpanPreservation(t *testing.T) {
	original := `动作细节 <d voice="s1">你好</d> 参考 @图片1 与 @图片2`
	if ok, reason := h3VerifySpanPreservation(original, `Rewritten action <d voice="s1">你好</d> with @图片2 and @图片1 refs`, h3ProtectedSpanPatterns); !ok {
		t.Fatalf("verbatim reorder should pass, reason=%s", reason)
	}
	if ok, _ := h3VerifySpanPreservation(original, `骨架化摘要丢掉台词 @图片1 @图片2`, h3ProtectedSpanPatterns); ok {
		t.Fatal("dropped d-block must fail")
	}
	if ok, _ := h3VerifySpanPreservation(original, `复制膨胀 @图片1 与 @图片1 与 @图片2 <d voice="s1">你好</d>`, h3ProtectedSpanPatterns); ok {
		t.Fatal("count inflation must fail")
	}
	if ok, _ := h3VerifySpanPreservation(original, `篡改台词 <d voice="s1">你好吗</d> @图片1 @图片2`, h3ProtectedSpanPatterns); ok {
		t.Fatal("mutated d-block must fail")
	}
	if ok, reason := h3VerifySpanPreservation(original, "  ", h3ProtectedSpanPatterns); ok || reason != "empty_rewrite" {
		t.Fatalf("empty rewrite must fail with empty_rewrite, got ok=%v reason=%s", ok, reason)
	}
	// 原文无 span：任何非空改写都通过。
	if ok, reason := h3VerifySpanPreservation("plain prompt", "rewritten plain", h3ProtectedSpanPatterns); !ok {
		t.Fatalf("spanless prompt must pass, reason=%s", reason)
	}
}

func TestMaybeRewriteVideoPromptH3Degrades(t *testing.T) {
	original := `动作 <d voice="s1">台词</d> @图片1`
	t.Setenv("CANVAS_H3_REWRITE_ENABLED", "true")

	t.Run("disabled_toggle_noops", func(t *testing.T) {
		t.Setenv("CANVAS_H3_REWRITE_ENABLED", "false")
		rewritten, skip := (&Service{}).maybeRewriteVideoPromptH3(context.Background(), original, nil)
		if rewritten != "" || skip != "" {
			t.Fatalf("toggle off must no-op, got %q / %q", rewritten, skip)
		}
	})

	t.Run("h3_formatted_noops", func(t *testing.T) {
		rewritten, skip := (&Service{}).maybeRewriteVideoPromptH3(context.Background(), original, map[string]interface{}{"h3Formatted": true})
		if rewritten != "" || skip != "" {
			t.Fatalf("h3Formatted prompt must skip silently, got %q / %q", rewritten, skip)
		}
	})

	t.Run("h3_signature_noops", func(t *testing.T) {
		// 分镜行 videoPrompt（H3 三段骨架产物）即使没带 metadata 标记也不得二次改写。
		h3Prompt := `[Shot 1] Live-action. integrated_multimodal_description: medium shot… <d voice="s1">台词</d> @图片1`
		rewritten, skip := (&Service{}).maybeRewriteVideoPromptH3(context.Background(), h3Prompt, nil)
		if rewritten != "" || skip != "" {
			t.Fatalf("H3-formatted signature prompt must skip silently, got %q / %q", rewritten, skip)
		}
	})

	t.Run("model_unavailable_degrades", func(t *testing.T) {
		rewritten, skip := newH3RewriteTestService(t).maybeRewriteVideoPromptH3(context.Background(), original, nil)
		if rewritten != "" || !strings.HasPrefix(skip, "no_rewrite_model:") {
			t.Fatalf("missing model must degrade, got %q / %q", rewritten, skip)
		}
	})

	t.Run("llm_error_degrades", func(t *testing.T) {
		prev := h3RewriteChatCall
		defer func() { h3RewriteChatCall = prev }()
		prevResolve := h3RewriteResolveConfig
		defer func() { h3RewriteResolveConfig = prevResolve }()
		h3RewriteResolveConfig = func(s *Service) (providerConfig, error) { return providerConfig{Model: "stub"}, nil }
		h3RewriteChatCall = func(ctx context.Context, config providerConfig, prompt string) (string, error) {
			return "", errors.New("503 busy")
		}
		rewritten, skip := newH3RewriteTestService(t).maybeRewriteVideoPromptH3(context.Background(), original, nil)
		if rewritten != "" || !strings.Contains(skip, "rewrite_failed:503 busy") {
			t.Fatalf("llm error must degrade, got %q / %q", rewritten, skip)
		}
	})

	t.Run("span_failure_degrades", func(t *testing.T) {
		prev := h3RewriteChatCall
		defer func() { h3RewriteChatCall = prev }()
		prevResolve := h3RewriteResolveConfig
		defer func() { h3RewriteResolveConfig = prevResolve }()
		h3RewriteResolveConfig = func(s *Service) (providerConfig, error) { return providerConfig{Model: "stub"}, nil }
		h3RewriteChatCall = func(ctx context.Context, config providerConfig, prompt string) (string, error) {
			return "骨架化输出丢了台词", nil
		}
		rewritten, skip := newH3RewriteTestService(t).maybeRewriteVideoPromptH3(context.Background(), original, nil)
		if rewritten != "" || !strings.HasPrefix(skip, "span_verification_failed:") {
			t.Fatalf("span failure must degrade, got %q / %q", rewritten, skip)
		}
	})

	t.Run("success_returns_rewrite", func(t *testing.T) {
		prev := h3RewriteChatCall
		defer func() { h3RewriteChatCall = prev }()
		prevResolve := h3RewriteResolveConfig
		defer func() { h3RewriteResolveConfig = prevResolve }()
		h3RewriteResolveConfig = func(s *Service) (providerConfig, error) { return providerConfig{Model: "stub"}, nil }
		h3RewriteChatCall = func(ctx context.Context, config providerConfig, prompt string) (string, error) {
			if !strings.Contains(prompt, "【输入提示词】") || !strings.Contains(prompt, original) {
				t.Fatalf("call prompt must carry instructions+original, got %q", prompt)
			}
			return `English rewrite <d voice="s1">台词</d> @图片1`, nil
		}
		rewritten, skip := newH3RewriteTestService(t).maybeRewriteVideoPromptH3(context.Background(), original, nil)
		if skip != "" || !strings.Contains(rewritten, `<d voice="s1">台词</d>`) {
			t.Fatalf("success must return rewrite, got %q / %q", rewritten, skip)
		}
	})
}

func TestCanvasH3RewriteEnabledDefault(t *testing.T) {
	t.Setenv("CANVAS_H3_REWRITE_ENABLED", "")
	if !canvasH3RewriteEnabled() {
		t.Fatal("G1=PASS: default must be enabled")
	}
	t.Setenv("CANVAS_H3_REWRITE_ENABLED", "0")
	if canvasH3RewriteEnabled() {
		t.Fatal("explicit 0 must disable")
	}
}
