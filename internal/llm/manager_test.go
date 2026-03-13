package llm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type stubProvider struct {
	name string
	run  func(context.Context, string) ([]string, string, error)
}

func (s stubProvider) Name() string {
	return s.name
}

func (s stubProvider) Expand(ctx context.Context, prompt string) ([]string, string, error) {
	return s.run(ctx, prompt)
}

func TestExpandWithFallbackContinuesAfterTimeout(t *testing.T) {
	previousTimeout := providerAttemptTimeout
	providerAttemptTimeout = 20 * time.Millisecond
	defer func() {
		providerAttemptTimeout = previousTimeout
	}()

	manager := &Manager{
		providers: map[string]Provider{
			"chatgpt": stubProvider{
				name: "chatgpt",
				run: func(ctx context.Context, prompt string) ([]string, string, error) {
					<-ctx.Done()
					return nil, "", ctx.Err()
				},
			},
			"gemini": stubProvider{
				name: "gemini",
				run: func(ctx context.Context, prompt string) ([]string, string, error) {
					return []string{"bmx tricks"}, `["bmx tricks"]`, nil
				},
			},
		},
	}

	var progress []string
	result, err := manager.ExpandWithFallback(context.Background(), []string{"chatgpt", "gemini"}, "prompt", func(provider, state, detail string) {
		progress = append(progress, provider+":"+state)
	})
	if err != nil {
		t.Fatalf("ExpandWithFallback failed: %v", err)
	}
	if result.Provider != "gemini" {
		t.Fatalf("expected gemini to succeed, got %q", result.Provider)
	}
	if len(result.Keywords) != 1 || result.Keywords[0] != "bmx tricks" {
		t.Fatalf("unexpected keywords: %#v", result.Keywords)
	}
	want := []string{"chatgpt:start", "chatgpt:failed", "gemini:start", "gemini:success"}
	if strings.Join(progress, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected progress: got %v want %v", progress, want)
	}
}

func TestExpandWithFallbackAggregatesFailures(t *testing.T) {
	manager := &Manager{
		providers: map[string]Provider{
			"chatgpt": stubProvider{
				name: "chatgpt",
				run: func(ctx context.Context, prompt string) ([]string, string, error) {
					return nil, "", errors.New("selector missing")
				},
			},
		},
	}

	_, err := manager.ExpandWithFallback(context.Background(), []string{"chatgpt"}, "prompt", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "selector missing") {
		t.Fatalf("expected aggregated provider error, got %v", err)
	}
}

func TestFindBrowserBinaryUsesEnvOverride(t *testing.T) {
	tempDir := t.TempDir()
	browserPath := filepath.Join(tempDir, "edge.exe")
	if err := os.WriteFile(browserPath, []byte("stub"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	previous := os.Getenv("LLM_BROWSER_BIN")
	if err := os.Setenv("LLM_BROWSER_BIN", browserPath); err != nil {
		t.Fatalf("Setenv failed: %v", err)
	}
	defer func() {
		_ = os.Setenv("LLM_BROWSER_BIN", previous)
	}()

	if got := findBrowserBinary(); got != browserPath {
		t.Fatalf("expected env browser path %q, got %q", browserPath, got)
	}
}

func TestBrowserPathCandidatesWindowsPrefersEdge(t *testing.T) {
	candidates := browserPathCandidates("windows")
	if len(candidates) < 2 {
		t.Fatalf("expected windows candidates, got %v", candidates)
	}
	if !strings.Contains(strings.ToLower(candidates[0]), "msedge.exe") {
		t.Fatalf("expected edge first, got %q", candidates[0])
	}
}
