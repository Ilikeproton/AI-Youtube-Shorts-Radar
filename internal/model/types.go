package model

import "time"

type Settings struct {
	YouTubeAPIKey string   `json:"youtubeApiKey"`
	ProxyURL      string   `json:"proxyUrl"`
	ProviderOrder []string `json:"providerOrder"`
	DefaultMarket string   `json:"defaultMarket"`
}

type CreateBatchRequest struct {
	Name                string   `json:"name"`
	SeedKeywords        []string `json:"seedKeywords"`
	PublishedWithinDays int      `json:"publishedWithinDays"`
	KeywordCap          int      `json:"keywordCap"`
	HidePreviouslySeen  bool     `json:"hidePreviouslySeen"`
}

type Batch struct {
	ID                  int64      `json:"id"`
	Name                string     `json:"name"`
	Status              string     `json:"status"`
	CreatedAt           time.Time  `json:"createdAt"`
	StartedAt           *time.Time `json:"startedAt,omitempty"`
	FinishedAt          *time.Time `json:"finishedAt,omitempty"`
	PublishedWithinDays int        `json:"publishedWithinDays"`
	KeywordCap          int        `json:"keywordCap"`
	HidePreviouslySeen  bool       `json:"hidePreviouslySeen"`
	SearchQuotaUnits    int        `json:"searchQuotaUnits"`
	ErrorSummary        string     `json:"errorSummary"`
	TotalResults        int        `json:"totalResults"`
	SeedKeywords        []string   `json:"seedKeywords,omitempty"`
	SuggestedKeywords   []string   `json:"suggestedKeywords,omitempty"`
}

type BatchResult struct {
	VideoID         string    `json:"videoId"`
	Thumbnail       string    `json:"thumbnail"`
	Title           string    `json:"title"`
	Channel         string    `json:"channel"`
	PublishedAt     time.Time `json:"publishedAt"`
	Views           int64     `json:"views"`
	ViewsPerDay     float64   `json:"viewsPerDay"`
	BreakoutScore   float64   `json:"breakoutScore"`
	SourceKeyword   string    `json:"sourceKeyword"`
	WatchLink       string    `json:"watchLink"`
	ShortsLink      string    `json:"shortsLink"`
	DuplicateSeen   bool      `json:"duplicateSeen"`
	HiddenByDefault bool      `json:"hiddenByDefault"`
	VerifiedShort   string    `json:"verifiedShort"`
}

type JobEvent struct {
	ID        int64     `json:"id"`
	BatchID   int64     `json:"batchId"`
	Sequence  int       `json:"sequence"`
	Level     string    `json:"level"`
	Stage     string    `json:"stage"`
	Message   string    `json:"message"`
	Payload   string    `json:"payload,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

type KeywordRecord struct {
	Term     string `json:"term"`
	Phase    string `json:"phase"`
	Provider string `json:"provider,omitempty"`
	Position int    `json:"position"`
}

type VideoRecord struct {
	VideoID          string
	WatchURL         string
	ShortsURL        string
	Title            string
	ChannelTitle     string
	PublishedAt      time.Time
	DurationSec      int
	Views            int64
	VerifiedShort    string
	ThumbnailURL     string
	LastSeenAt       time.Time
	FirstSeenBatchID int64
}

type BatchHit struct {
	BatchID         int64
	VideoID         string
	SourceKeyword   string
	DuplicateSeen   bool
	HiddenByDefault bool
	AgeDays         int
	ViewsPerDay     float64
	BreakoutScore   float64
	CreatedAt       time.Time
}

type BatchStatusResponse struct {
	Batch  Batch      `json:"batch"`
	Events []JobEvent `json:"events"`
}

type SearchHit struct {
	VideoID string
}

type VideoDetail struct {
	VideoID      string
	Title        string
	ChannelTitle string
	PublishedAt  time.Time
	DurationSec  int
	Views        int64
	ThumbnailURL string
	WatchURL     string
	ShortsURL    string
}
