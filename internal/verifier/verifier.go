package verifier

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

const (
	StatusTrue    = "true"
	StatusFalse   = "false"
	StatusUnknown = "unknown"
)

var (
	canonicalPattern = regexp.MustCompile(`(?i)<link[^>]+rel=["']canonical["'][^>]+href=["']([^"']+)["']`)
	ogURLPattern     = regexp.MustCompile(`(?i)<meta[^>]+property=["']og:url["'][^>]+content=["']([^"']+)["']`)
)

type Verifier struct {
	httpClient *http.Client
	baseURL    string
}

func New(httpClient *http.Client) *Verifier {
	return &Verifier{
		httpClient: httpClient,
		baseURL:    "https://www.youtube.com",
	}
}

func (v *Verifier) SetBaseURL(baseURL string) {
	v.baseURL = strings.TrimRight(baseURL, "/")
}

func (v *Verifier) Verify(ctx context.Context, videoID string) (string, error) {
	endpoint := fmt.Sprintf("%s/shorts/%s", v.baseURL, videoID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return StatusUnknown, err
	}
	response, err := v.httpClient.Do(request)
	if err != nil {
		return StatusUnknown, err
	}
	defer response.Body.Close()

	if response.StatusCode >= 300 {
		return StatusUnknown, fmt.Errorf("short verifier failed: %s", response.Status)
	}

	bodyBytes, err := io.ReadAll(response.Body)
	if err != nil {
		return StatusUnknown, err
	}
	body := string(bodyBytes)
	for _, pattern := range []*regexp.Regexp{canonicalPattern, ogURLPattern} {
		matches := pattern.FindStringSubmatch(body)
		if len(matches) != 2 {
			continue
		}
		if strings.Contains(matches[1], "/shorts/"+videoID) {
			return StatusTrue, nil
		}
		if strings.Contains(matches[1], videoID) {
			return StatusFalse, nil
		}
	}
	return StatusUnknown, fmt.Errorf("short verifier could not detect canonical for %s", videoID)
}
