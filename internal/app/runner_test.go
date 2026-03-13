package app

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"youtubeshort/internal/events"
	"youtubeshort/internal/llm"
	"youtubeshort/internal/model"
	"youtubeshort/internal/store"
	"youtubeshort/internal/verifier"
)

type fakeYouTube struct {
	now time.Time
}

func (f fakeYouTube) SearchVideos(context.Context, string, string, time.Time, int) ([]model.SearchHit, int, error) {
	return []model.SearchHit{{VideoID: "abc123"}}, 100, nil
}

func (f fakeYouTube) VideoDetails(context.Context, []string) ([]model.VideoDetail, int, error) {
	return []model.VideoDetail{{
		VideoID:      "abc123",
		Title:        "Soccer Dribble Challenge",
		ChannelTitle: "Creator",
		PublishedAt:  f.now.Add(-48 * time.Hour),
		DurationSec:  60,
		Views:        150000,
		ThumbnailURL: "https://example.com/thumb.jpg",
		WatchURL:     "https://youtube.com/watch?v=abc123",
		ShortsURL:    "https://youtube.com/shorts/abc123",
	}}, 1, nil
}

type recordingYouTube struct {
	now       time.Time
	searched  []string
	searchHit chan struct{}
}

func (f *recordingYouTube) SearchVideos(context.Context, string, string, time.Time, int) ([]model.SearchHit, int, error) {
	f.searched = append(f.searched, "seed")
	select {
	case <-f.searchHit:
	default:
		close(f.searchHit)
	}
	return []model.SearchHit{{VideoID: "abc123"}}, 100, nil
}

func (f *recordingYouTube) VideoDetails(context.Context, []string) ([]model.VideoDetail, int, error) {
	return []model.VideoDetail{{
		VideoID:      "abc123",
		Title:        "Soccer Dribble Challenge",
		ChannelTitle: "Creator",
		PublishedAt:  f.now.Add(-48 * time.Hour),
		DurationSec:  60,
		Views:        150000,
		ThumbnailURL: "https://example.com/thumb.jpg",
		WatchURL:     "https://youtube.com/watch?v=abc123",
		ShortsURL:    "https://youtube.com/shorts/abc123",
	}}, 1, nil
}

type blockingYouTube struct {
	searchHit chan struct{}
}

func (f *blockingYouTube) SearchVideos(ctx context.Context, keyword, language string, publishedAfter time.Time, maxResults int) ([]model.SearchHit, int, error) {
	select {
	case <-f.searchHit:
	default:
		close(f.searchHit)
	}
	<-ctx.Done()
	return nil, 0, ctx.Err()
}

func (f *blockingYouTube) VideoDetails(context.Context, []string) ([]model.VideoDetail, int, error) {
	return nil, 0, nil
}

type detailBlockingYouTube struct {
	now         time.Time
	mu          sync.Mutex
	searchCalls map[string]int
	detailHit   chan struct{}
}

func (f *detailBlockingYouTube) SearchVideos(context.Context, string, string, time.Time, int) ([]model.SearchHit, int, error) {
	return []model.SearchHit{{VideoID: "abc123"}}, 100, nil
}

func (f *detailBlockingYouTube) VideoDetails(ctx context.Context, ids []string) ([]model.VideoDetail, int, error) {
	f.mu.Lock()
	if f.searchCalls == nil {
		f.searchCalls = map[string]int{}
	}
	f.searchCalls["details"]++
	f.mu.Unlock()
	select {
	case <-f.detailHit:
	default:
		close(f.detailHit)
	}
	<-ctx.Done()
	return nil, 0, ctx.Err()
}

type countingYouTube struct {
	now         time.Time
	mu          sync.Mutex
	searchCalls map[string]int
}

func (f *countingYouTube) SearchVideos(context.Context, string, string, time.Time, int) ([]model.SearchHit, int, error) {
	f.mu.Lock()
	if f.searchCalls == nil {
		f.searchCalls = map[string]int{}
	}
	f.searchCalls["search"]++
	f.mu.Unlock()
	return []model.SearchHit{{VideoID: "abc123"}}, 100, nil
}

func (f *countingYouTube) VideoDetails(context.Context, []string) ([]model.VideoDetail, int, error) {
	return []model.VideoDetail{{
		VideoID:      "abc123",
		Title:        "Soccer Dribble Challenge",
		ChannelTitle: "Creator",
		PublishedAt:  f.now.Add(-48 * time.Hour),
		DurationSec:  60,
		Views:        150000,
		ThumbnailURL: "https://example.com/thumb.jpg",
		WatchURL:     "https://youtube.com/watch?v=abc123",
		ShortsURL:    "https://youtube.com/shorts/abc123",
	}}, 1, nil
}

type fakeVerifier struct{}

func (fakeVerifier) Verify(context.Context, string) (string, error) {
	return verifier.StatusTrue, nil
}

func TestBatchRunnerDeduplicatesVideosAcrossBatches(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	st, err := store.New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("store.New failed: %v", err)
	}
	defer st.Close()

	if err := st.UpdateSettings(context.Background(), model.Settings{
		YouTubeAPIKey: "test-key",
		ProxyURL:      "socks5://127.0.0.1:10625",
		ProviderOrder: []string{"chatgpt"},
		DefaultMarket: "en",
	}); err != nil {
		t.Fatalf("UpdateSettings failed: %v", err)
	}

	runner := &BatchRunner{
		store:    st,
		broker:   events.NewBroker(),
		llm:      fakeLLMAdapter{},
		youtube:  fakeYouTube{now: now},
		verifier: fakeVerifier{},
		now:      func() time.Time { return now },
	}

	req := model.CreateBatchRequest{
		Name:                "First",
		SeedKeywords:        []string{"soccer dribble"},
		PublishedWithinDays: 90,
		KeywordCap:          1,
		HidePreviouslySeen:  true,
	}
	batch1, err := st.CreateBatch(context.Background(), req, now)
	if err != nil {
		t.Fatalf("CreateBatch batch1 failed: %v", err)
	}
	runner.execute(batch1, req)

	req.Name = "Second"
	batch2, err := st.CreateBatch(context.Background(), req, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("CreateBatch batch2 failed: %v", err)
	}
	runner.execute(batch2, req)

	videoCount, err := st.CountVideos(context.Background())
	if err != nil {
		t.Fatalf("CountVideos failed: %v", err)
	}
	if videoCount != 1 {
		t.Fatalf("expected 1 stored video, got %d", videoCount)
	}

	hitCount, err := st.CountBatchHits(context.Background())
	if err != nil {
		t.Fatalf("CountBatchHits failed: %v", err)
	}
	if hitCount != 2 {
		t.Fatalf("expected 2 batch hits, got %d", hitCount)
	}

	results, err := st.ListBatchResults(context.Background(), batch2.ID, "score")
	if err != nil {
		t.Fatalf("ListBatchResults failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].DuplicateSeen || !results[0].HiddenByDefault {
		t.Fatalf("expected second batch result to be marked duplicate and hidden, got %#v", results[0])
	}
}

func TestBatchRunnerSearchesSeedsBeforeLLMExpansion(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	st, err := store.New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("store.New failed: %v", err)
	}
	defer st.Close()

	if err := st.UpdateSettings(context.Background(), model.Settings{
		YouTubeAPIKey: "test-key",
		ProxyURL:      "socks5://127.0.0.1:10625",
		ProviderOrder: []string{"chatgpt"},
		DefaultMarket: "en",
	}); err != nil {
		t.Fatalf("UpdateSettings failed: %v", err)
	}

	searchHit := make(chan struct{})
	youtube := &recordingYouTube{now: now, searchHit: searchHit}
	runner := &BatchRunner{
		store:    st,
		broker:   events.NewBroker(),
		llm:      blockingLLMAdapter{searchHit: searchHit},
		youtube:  youtube,
		verifier: fakeVerifier{},
		now:      func() time.Time { return now },
	}

	req := model.CreateBatchRequest{
		Name:                "Order Check",
		SeedKeywords:        []string{"soccer dribble"},
		PublishedWithinDays: 90,
		KeywordCap:          3,
		HidePreviouslySeen:  true,
	}
	batch, err := st.CreateBatch(context.Background(), req, now)
	if err != nil {
		t.Fatalf("CreateBatch failed: %v", err)
	}

	runner.execute(batch, req)

	if len(youtube.searched) == 0 {
		t.Fatal("expected youtube search to run before llm expansion")
	}
}

func TestBatchRunnerStopMarksBatchStopped(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	st, err := store.New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("store.New failed: %v", err)
	}
	defer st.Close()

	if err := st.UpdateSettings(context.Background(), model.Settings{
		YouTubeAPIKey: "test-key",
		ProxyURL:      "",
		ProviderOrder: []string{"chatgpt"},
		DefaultMarket: "en",
	}); err != nil {
		t.Fatalf("UpdateSettings failed: %v", err)
	}

	youtube := &blockingYouTube{searchHit: make(chan struct{})}
	runner := &BatchRunner{
		store:    st,
		broker:   events.NewBroker(),
		llm:      fakeLLMAdapter{},
		youtube:  youtube,
		verifier: fakeVerifier{},
		now:      func() time.Time { return now },
		cancels:  map[int64]context.CancelFunc{},
	}

	req := model.CreateBatchRequest{
		Name:                "Stop Me",
		SeedKeywords:        []string{"soccer dribble"},
		PublishedWithinDays: 90,
		KeywordCap:          3,
		HidePreviouslySeen:  true,
	}
	batch, err := st.CreateBatch(context.Background(), req, now)
	if err != nil {
		t.Fatalf("CreateBatch failed: %v", err)
	}

	runner.Start(batch, req)

	select {
	case <-youtube.searchHit:
	case <-time.After(2 * time.Second):
		t.Fatal("expected search to start before stopping batch")
	}

	if err := runner.Stop(batch.ID); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		current, err := st.GetBatch(context.Background(), batch.ID)
		if err != nil {
			t.Fatalf("GetBatch failed: %v", err)
		}
		if current.Status == "stopped" {
			if current.FinishedAt == nil {
				t.Fatal("expected finished_at to be set for stopped batch")
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}

	current, err := st.GetBatch(context.Background(), batch.ID)
	if err != nil {
		t.Fatalf("GetBatch failed: %v", err)
	}
	t.Fatalf("expected batch status stopped, got %q", current.Status)
}

func TestBatchRunnerResumeContinuesWithoutRepeatingSearch(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	st, err := store.New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("store.New failed: %v", err)
	}
	defer st.Close()

	if err := st.UpdateSettings(context.Background(), model.Settings{
		YouTubeAPIKey: "test-key",
		ProxyURL:      "",
		ProviderOrder: []string{"chatgpt"},
		DefaultMarket: "en",
	}); err != nil {
		t.Fatalf("UpdateSettings failed: %v", err)
	}

	initialYouTube := &detailBlockingYouTube{now: now, detailHit: make(chan struct{})}
	runner := &BatchRunner{
		store:    st,
		broker:   events.NewBroker(),
		llm:      fakeLLMAdapter{},
		youtube:  initialYouTube,
		verifier: fakeVerifier{},
		now:      func() time.Time { return now },
		cancels:  map[int64]context.CancelFunc{},
	}

	req := model.CreateBatchRequest{
		Name:                "Resume Me",
		SeedKeywords:        []string{"soccer dribble"},
		PublishedWithinDays: 90,
		KeywordCap:          3,
		HidePreviouslySeen:  true,
	}
	batch, err := st.CreateBatch(context.Background(), req, now)
	if err != nil {
		t.Fatalf("CreateBatch failed: %v", err)
	}

	runner.Start(batch, req)

	select {
	case <-initialYouTube.detailHit:
	case <-time.After(2 * time.Second):
		t.Fatal("expected detail fetch to start before stopping batch")
	}

	if err := runner.Stop(batch.ID); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		current, err := st.GetBatch(context.Background(), batch.ID)
		if err != nil {
			t.Fatalf("GetBatch failed: %v", err)
		}
		if current.Status == "stopped" {
			resumeYouTube := &countingYouTube{now: now}
			runner.youtube = resumeYouTube
			if err := runner.Resume(current); err != nil {
				t.Fatalf("Resume failed: %v", err)
			}

			completeDeadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(completeDeadline) {
				resumed, err := st.GetBatch(context.Background(), batch.ID)
				if err != nil {
					t.Fatalf("GetBatch after resume failed: %v", err)
				}
				if resumed.Status == "completed" {
					resumeYouTube.mu.Lock()
					searchCalls := resumeYouTube.searchCalls["search"]
					resumeYouTube.mu.Unlock()
					if searchCalls != 0 {
						t.Fatalf("expected resume to skip repeated youtube search, got %d search calls", searchCalls)
					}
					return
				}
				time.Sleep(25 * time.Millisecond)
			}
			t.Fatal("expected resumed batch to complete")
		}
		time.Sleep(25 * time.Millisecond)
	}

	t.Fatal("expected batch to reach stopped state before resume")
}

type fakeLLMAdapter struct{}

func (fakeLLMAdapter) ExpandWithFallback(context.Context, []string, string, llm.ProgressFn) (llm.ProviderResult, error) {
	return llm.ProviderResult{}, errors.New("llm unavailable")
}

type blockingLLMAdapter struct {
	searchHit <-chan struct{}
}

func (f blockingLLMAdapter) ExpandWithFallback(ctx context.Context, order []string, prompt string, progress llm.ProgressFn) (llm.ProviderResult, error) {
	select {
	case <-f.searchHit:
		return llm.ProviderResult{}, errors.New("llm unavailable")
	case <-time.After(150 * time.Millisecond):
		return llm.ProviderResult{}, errors.New("seed search did not happen before llm")
	case <-ctx.Done():
		return llm.ProviderResult{}, ctx.Err()
	}
}
