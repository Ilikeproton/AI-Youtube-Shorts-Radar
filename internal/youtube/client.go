package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"youtubeshort/internal/model"
)

const defaultBaseURL = "https://www.googleapis.com/youtube/v3"

type APIKeyResolver func(context.Context) (string, error)

type Client struct {
	httpClient *http.Client
	baseURL    string
	apiKey     APIKeyResolver
}

func NewClient(httpClient *http.Client, apiKey APIKeyResolver) *Client {
	return &Client{
		httpClient: httpClient,
		baseURL:    defaultBaseURL,
		apiKey:     apiKey,
	}
}

func (c *Client) SetBaseURL(baseURL string) {
	c.baseURL = strings.TrimRight(baseURL, "/")
}

func (c *Client) SearchVideos(ctx context.Context, keyword, language string, publishedAfter time.Time, maxResults int) ([]model.SearchHit, int, error) {
	apiKey, err := c.apiKey(ctx)
	if err != nil {
		return nil, 0, err
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, 0, errors.New("youtube api key is empty")
	}

	query := url.Values{}
	query.Set("key", apiKey)
	query.Set("part", "snippet")
	query.Set("type", "video")
	query.Set("order", "viewCount")
	query.Set("maxResults", strconv.Itoa(maxResults))
	query.Set("q", keyword)
	query.Set("relevanceLanguage", language)
	query.Set("publishedAfter", publishedAfter.UTC().Format(time.RFC3339))

	endpoint := fmt.Sprintf("%s/search?%s", c.baseURL, query.Encode())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()

	if response.StatusCode >= 300 {
		return nil, 0, fmt.Errorf("youtube search failed: %s", response.Status)
	}

	var payload struct {
		Items []struct {
			ID struct {
				VideoID string `json:"videoId"`
			} `json:"id"`
		} `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, 0, err
	}

	out := make([]model.SearchHit, 0, len(payload.Items))
	for _, item := range payload.Items {
		if strings.TrimSpace(item.ID.VideoID) == "" {
			continue
		}
		out = append(out, model.SearchHit{VideoID: item.ID.VideoID})
	}
	return out, 100, nil
}

func (c *Client) VideoDetails(ctx context.Context, ids []string) ([]model.VideoDetail, int, error) {
	if len(ids) == 0 {
		return nil, 0, nil
	}
	apiKey, err := c.apiKey(ctx)
	if err != nil {
		return nil, 0, err
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, 0, errors.New("youtube api key is empty")
	}

	query := url.Values{}
	query.Set("key", apiKey)
	query.Set("part", "snippet,contentDetails,statistics")
	query.Set("id", strings.Join(ids, ","))
	query.Set("maxResults", strconv.Itoa(len(ids)))

	endpoint := fmt.Sprintf("%s/videos?%s", c.baseURL, query.Encode())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()

	if response.StatusCode >= 300 {
		return nil, 0, fmt.Errorf("youtube videos failed: %s", response.Status)
	}

	var payload struct {
		Items []struct {
			ID             string `json:"id"`
			ContentDetails struct {
				Duration string `json:"duration"`
			} `json:"contentDetails"`
			Snippet struct {
				Title        string `json:"title"`
				ChannelTitle string `json:"channelTitle"`
				PublishedAt  string `json:"publishedAt"`
				Thumbnails   map[string]struct {
					URL string `json:"url"`
				} `json:"thumbnails"`
			} `json:"snippet"`
			Statistics struct {
				ViewCount string `json:"viewCount"`
			} `json:"statistics"`
		} `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, 0, err
	}

	out := make([]model.VideoDetail, 0, len(payload.Items))
	for _, item := range payload.Items {
		publishedAt, err := time.Parse(time.RFC3339, item.Snippet.PublishedAt)
		if err != nil {
			return nil, 0, err
		}
		durationSec, err := parseDurationSeconds(item.ContentDetails.Duration)
		if err != nil {
			return nil, 0, err
		}
		viewCount, err := strconv.ParseInt(item.Statistics.ViewCount, 10, 64)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, model.VideoDetail{
			VideoID:      item.ID,
			Title:        item.Snippet.Title,
			ChannelTitle: item.Snippet.ChannelTitle,
			PublishedAt:  publishedAt,
			DurationSec:  durationSec,
			Views:        viewCount,
			ThumbnailURL: selectThumbnail(item.Snippet.Thumbnails),
			WatchURL:     "https://www.youtube.com/watch?v=" + item.ID,
			ShortsURL:    "https://www.youtube.com/shorts/" + item.ID,
		})
	}
	return out, 1, nil
}

func selectThumbnail(thumbnails map[string]struct {
	URL string `json:"url"`
}) string {
	for _, key := range []string{"maxres", "high", "medium", "default"} {
		if value, ok := thumbnails[key]; ok && value.URL != "" {
			return value.URL
		}
	}
	for _, value := range thumbnails {
		if value.URL != "" {
			return value.URL
		}
	}
	return ""
}

func parseDurationSeconds(value string) (int, error) {
	if value == "" || value[0] != 'P' {
		return 0, fmt.Errorf("invalid duration %q", value)
	}

	var total int
	var current strings.Builder
	inTime := false
	for _, r := range value[1:] {
		switch {
		case r >= '0' && r <= '9':
			current.WriteRune(r)
		case r == 'T':
			inTime = true
		default:
			part, err := strconv.Atoi(current.String())
			if err != nil {
				return 0, err
			}
			current.Reset()
			switch r {
			case 'H':
				total += part * 3600
			case 'M':
				if inTime {
					total += part * 60
				} else {
					total += part * 86400 * 30
				}
			case 'S':
				total += part
			case 'D':
				total += part * 86400
			default:
				return 0, fmt.Errorf("unsupported duration unit %q", string(r))
			}
		}
	}
	return total, nil
}
