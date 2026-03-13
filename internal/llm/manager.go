package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"

	"youtubeshort/internal/keywords"
)

type Provider interface {
	Name() string
	Expand(ctx context.Context, prompt string) ([]string, string, error)
}

type ProgressFn func(provider, state, detail string)

type Manager struct {
	providers map[string]Provider
}

type ProviderResult struct {
	Provider string
	Keywords []string
	Raw      string
}

type WebConfig struct {
	Name              string
	URL               string
	InputSelectors    []string
	ResponseSelectors []string
}

type WebProvider struct {
	config      WebConfig
	proxyURL    string
	profilesDir string
	browserBin  string
}

var jsonArrayPattern = regexp.MustCompile(`(?s)\[[^\]]+\]`)
var providerAttemptTimeout = 35 * time.Second

func NewManager(proxyURL, profilesDir string) *Manager {
	browserBin := findBrowserBinary()
	providers := map[string]Provider{}
	for _, cfg := range defaultConfigs() {
		providers[cfg.Name] = &WebProvider{
			config:      cfg,
			proxyURL:    proxyURL,
			profilesDir: profilesDir,
			browserBin:  browserBin,
		}
	}
	return &Manager{providers: providers}
}

func (m *Manager) ExpandWithFallback(ctx context.Context, order []string, prompt string, progress ProgressFn) (ProviderResult, error) {
	failures := make([]string, 0, len(order))
	for _, name := range order {
		provider, ok := m.providers[strings.ToLower(strings.TrimSpace(name))]
		if !ok {
			continue
		}
		if progress != nil {
			progress(provider.Name(), "start", "")
		}
		providerCtx, cancel := context.WithTimeout(ctx, providerAttemptTimeout)
		keywords, raw, err := expandWithTimeout(providerCtx, provider, prompt)
		cancel()
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", provider.Name(), err))
			if progress != nil {
				progress(provider.Name(), "failed", err.Error())
			}
			continue
		}
		if progress != nil {
			progress(provider.Name(), "success", fmt.Sprintf("%d keywords", len(keywords)))
		}
		return ProviderResult{
			Provider: provider.Name(),
			Keywords: keywords,
			Raw:      raw,
		}, nil
	}
	if len(failures) == 0 {
		return ProviderResult{}, errors.New("all llm providers failed")
	}
	return ProviderResult{}, fmt.Errorf("all llm providers failed: %s", strings.Join(failures, "; "))
}

func (w *WebProvider) Name() string {
	return w.config.Name
}

func (w *WebProvider) Expand(ctx context.Context, prompt string) ([]string, string, error) {
	profileDir := filepath.Join(w.profilesDir, w.config.Name)
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		return nil, "", err
	}

	launch := launcher.New().
		Leakless(false).
		Headless(false).
		UserDataDir(profileDir).
		Set("window-position", "-32000,-32000").
		Set("window-size", "1280,900").
		Set("disable-blink-features", "AutomationControlled")
	if strings.TrimSpace(w.browserBin) != "" {
		launch = launch.Bin(w.browserBin)
	}
	if strings.TrimSpace(w.proxyURL) != "" {
		launch = launch.Set("proxy-server", w.proxyURL)
	}

	controlURL, err := launch.Launch()
	if err != nil {
		return nil, "", err
	}
	defer launch.Cleanup()

	browser := rod.New().ControlURL(controlURL)
	if err := browser.Connect(); err != nil {
		return nil, "", err
	}
	defer browser.Close()
	stopClose := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = browser.Close()
		case <-stopClose:
		}
	}()
	defer close(stopClose)

	page, err := browser.Page(proto.TargetCreateTarget{URL: w.config.URL})
	if err != nil {
		return nil, "", fmt.Errorf("%s open page failed: %w", w.config.Name, err)
	}
	defer page.Close()

	if err := page.Timeout(12 * time.Second).WaitLoad(); err != nil {
		return nil, "", fmt.Errorf("%s wait load failed: %w", w.config.Name, err)
	}
	_ = page.Timeout(2500 * time.Millisecond).WaitIdle(1500 * time.Millisecond)
	_ = dismissConsent(page)

	element, err := firstElement(page, w.config.InputSelectors)
	if err != nil {
		if wallErr := rejectLoginWall(page); wallErr != nil {
			return nil, "", fmt.Errorf("%s: %w", w.config.Name, wallErr)
		}
		return nil, "", fmt.Errorf("%s input selector not found: %w", w.config.Name, err)
	}
	if err := element.Focus(); err != nil {
		return nil, "", fmt.Errorf("%s focus input failed: %w", w.config.Name, err)
	}
	if err := page.InsertText(prompt); err != nil {
		return nil, "", fmt.Errorf("%s insert prompt failed: %w", w.config.Name, err)
	}
	if err := page.Keyboard.Press(input.Enter); err != nil {
		return nil, "", fmt.Errorf("%s submit prompt failed: %w", w.config.Name, err)
	}

	deadline := time.Now().Add(30 * time.Second)
	var raw string
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, raw, ctx.Err()
		default:
		}

		raw, _ = lastResponseText(page, w.config.ResponseSelectors)
		if array := jsonArrayPattern.FindString(raw); array != "" {
			keywords, err := decodeArray(array)
			if err == nil && len(keywords) > 0 {
				return keywords, raw, nil
			}
		}
		time.Sleep(1500 * time.Millisecond)
	}

	if raw == "" {
		raw, _ = lastResponseText(page, []string{"body"})
	}
	if strings.TrimSpace(raw) == "" {
		return nil, raw, fmt.Errorf("%s returned no readable response text", w.config.Name)
	}
	return nil, raw, fmt.Errorf("%s did not return a JSON array", w.config.Name)
}

type providerExpandResult struct {
	keywords []string
	raw      string
	err      error
}

func expandWithTimeout(ctx context.Context, provider Provider, prompt string) ([]string, string, error) {
	resultCh := make(chan providerExpandResult, 1)
	go func() {
		keywords, raw, err := provider.Expand(ctx, prompt)
		resultCh <- providerExpandResult{
			keywords: keywords,
			raw:      raw,
			err:      err,
		}
	}()

	select {
	case <-ctx.Done():
		return nil, "", ctx.Err()
	case result := <-resultCh:
		return result.keywords, result.raw, result.err
	}
}

func defaultConfigs() []WebConfig {
	return []WebConfig{
		{
			Name:              "chatgpt",
			URL:               "https://chatgpt.com/",
			InputSelectors:    []string{"#prompt-textarea", "textarea", "[contenteditable='true']"},
			ResponseSelectors: []string{"article", "[data-message-author-role='assistant']", ".markdown", "main"},
		},
		{
			Name:              "gemini",
			URL:               "https://gemini.google.com/app",
			InputSelectors:    []string{"textarea", "[contenteditable='true']", "rich-textarea textarea"},
			ResponseSelectors: []string{"message-content", ".model-response-text", "main"},
		},
		{
			Name:              "copilot",
			URL:               "https://copilot.microsoft.com/",
			InputSelectors:    []string{"textarea", "[contenteditable='true']", "input"},
			ResponseSelectors: []string{"cib-message-group", "main"},
		},
		{
			Name:              "perplexity",
			URL:               "https://www.perplexity.ai/",
			InputSelectors:    []string{"textarea", "[contenteditable='true']", "input"},
			ResponseSelectors: []string{"main", "[data-testid='copilot-answer']", ".prose"},
		},
	}
}

func BuildPrompt(seeds []string, current []string) string {
	parts := keywords.MergeUnique(seeds, current)
	parts = keywords.Cap(parts, 4)
	return fmt.Sprintf(
		"Topic: %s. Return 6 short English YouTube Shorts search phrases as JSON array only. Max 4 words each.",
		strings.Join(parts, ", "),
	)
}

func decodeArray(raw string) ([]string, error) {
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, err
	}
	values = keywords.MergeUnique(values)
	values = slices.DeleteFunc(values, func(item string) bool {
		return strings.TrimSpace(item) == ""
	})
	return values, nil
}

func firstElement(page *rod.Page, selectors []string) (*rod.Element, error) {
	for _, selector := range selectors {
		element, err := page.Timeout(4 * time.Second).Element(selector)
		if err == nil {
			return element, nil
		}
	}
	return nil, errors.New("provider input selector not found")
}

func lastResponseText(page *rod.Page, selectors []string) (string, error) {
	for _, selector := range selectors {
		elements, err := page.Elements(selector)
		if err != nil || len(elements) == 0 {
			continue
		}
		for index := len(elements) - 1; index >= 0; index-- {
			text, err := elements[index].Text()
			if err == nil && strings.TrimSpace(text) != "" {
				return text, nil
			}
		}
	}
	return "", errors.New("no response found")
}

func rejectLoginWall(page *rod.Page) error {
	body, err := page.Element("body")
	if err != nil {
		return nil
	}
	text, err := body.Text()
	if err != nil {
		return nil
	}
	lower := strings.ToLower(text)
	if strings.Contains(lower, "before you continue to google") {
		return errors.New("provider consent page is still blocking access")
	}
	if strings.Contains(lower, "verify you are human") || strings.Contains(lower, "captcha") || strings.Contains(lower, "turnstile") {
		return errors.New("provider requires login or verification")
	}
	if strings.Contains(lower, "sign in") || strings.Contains(lower, "log in") {
		return errors.New("provider requires login or verification")
	}
	return nil
}

func dismissConsent(page *rod.Page) error {
	for _, selector := range []string{"button", "[role='button']"} {
		elements, err := page.Elements(selector)
		if err != nil || len(elements) == 0 {
			continue
		}
		for _, element := range elements {
			text, err := element.Text()
			if err != nil {
				continue
			}
			label := strings.ToLower(strings.TrimSpace(text))
			if !strings.Contains(label, "accept all") && !strings.Contains(label, "i agree") && label != "accept" {
				continue
			}
			if err := element.Click(proto.InputMouseButtonLeft, 1); err == nil {
				time.Sleep(2 * time.Second)
				_ = page.Timeout(2 * time.Second).WaitIdle(800 * time.Millisecond)
				return nil
			}
		}
	}
	return nil
}

func findBrowserBinary() string {
	if value := strings.TrimSpace(os.Getenv("LLM_BROWSER_BIN")); value != "" {
		if _, err := os.Stat(value); err == nil {
			return value
		}
	}

	for _, candidate := range browserPathCandidates(runtime.GOOS) {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	for _, command := range browserCommandCandidates(runtime.GOOS) {
		if path, err := exec.LookPath(command); err == nil && strings.TrimSpace(path) != "" {
			return path
		}
	}
	return ""
}

func browserPathCandidates(goos string) []string {
	if goos == "windows" {
		return []string{
			`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		}
	}
	return nil
}

func browserCommandCandidates(goos string) []string {
	if goos == "windows" {
		return []string{"msedge.exe", "chrome.exe"}
	}
	return []string{"microsoft-edge", "msedge", "google-chrome", "chrome", "chromium", "chromium-browser"}
}
