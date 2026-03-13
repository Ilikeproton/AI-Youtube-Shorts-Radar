package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"youtubeshort/internal/config"
	"youtubeshort/internal/events"
	"youtubeshort/internal/httpapi"
	"youtubeshort/internal/keywords"
	"youtubeshort/internal/llm"
	"youtubeshort/internal/model"
	"youtubeshort/internal/netutil"
	"youtubeshort/internal/ranking"
	"youtubeshort/internal/store"
	"youtubeshort/internal/verifier"
	"youtubeshort/internal/window"
	"youtubeshort/internal/youtube"
)

type BatchRunner struct {
	store       *store.Store
	broker      *events.Broker
	llm         keywordExpander
	youtube     youtubeService
	verifier    shortVerifier
	profilesDir string
	now         func() time.Time
	mu          sync.Mutex
	cancels     map[int64]context.CancelFunc
}

type batchProgress struct {
	ruleKeywords      []string
	llmKeywords       []string
	finalKeywords     []string
	llmCompleted      bool
	searchedTerms     map[string]struct{}
	videoToKeywords   map[string][]string
	allCandidateIDs   []string
	processedVideoIDs map[string]struct{}
}

type keywordExpander interface {
	ExpandWithFallback(ctx context.Context, order []string, prompt string, progress llm.ProgressFn) (llm.ProviderResult, error)
}

type youtubeService interface {
	SearchVideos(ctx context.Context, keyword, language string, publishedAfter time.Time, maxResults int) ([]model.SearchHit, int, error)
	VideoDetails(ctx context.Context, ids []string) ([]model.VideoDetail, int, error)
}

type shortVerifier interface {
	Verify(ctx context.Context, videoID string) (string, error)
}

func Run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	db, err := store.New(cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.RecoverInterruptedBatches(context.Background(), time.Now()); err != nil {
		return err
	}

	broker := events.NewBroker()
	runner := &BatchRunner{
		store:       db,
		broker:      broker,
		profilesDir: cfg.ProfilesDir,
		now:         time.Now,
		cancels:     map[int64]context.CancelFunc{},
	}

	server := httpapi.NewServer(cfg, db, runner, broker)
	httpServer := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: server.Handler(),
	}

	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return err
	}
	defer listener.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("http server stopped: %v", err)
			stop()
		}
	}()

	uiURL := fmt.Sprintf("http://%s", listener.Addr().String())
	if cfg.IsWindows && !cfg.NoWindow {
		if err := window.Open(uiURL, cfg); err != nil {
			log.Printf("window host failed: %v", err)
		}
		stop()
	} else {
		log.Printf("UI available at %s", uiURL)
		<-ctx.Done()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

func (r *BatchRunner) Start(batch model.Batch, req model.CreateBatchRequest) {
	runCtx, cancel := context.WithCancel(context.Background())
	r.storeCancel(batch.ID, cancel)
	go func() {
		r.executeWithContext(runCtx, batch, req, false)
	}()
}

func (r *BatchRunner) execute(batch model.Batch, req model.CreateBatchRequest) {
	r.executeWithContext(context.Background(), batch, req, false)
}

func (r *BatchRunner) Resume(batch model.Batch) error {
	if len(batch.SeedKeywords) == 0 {
		return errors.New("batch has no seed keywords to resume")
	}
	if err := r.store.PrepareBatchResume(context.Background(), batch.ID); err != nil {
		return err
	}

	req := model.CreateBatchRequest{
		Name:                batch.Name,
		SeedKeywords:        batch.SeedKeywords,
		PublishedWithinDays: batch.PublishedWithinDays,
		KeywordCap:          batch.KeywordCap,
		HidePreviouslySeen:  batch.HidePreviouslySeen,
	}

	runCtx, cancel := context.WithCancel(context.Background())
	r.storeCancel(batch.ID, cancel)
	go func() {
		r.executeWithContext(runCtx, batch, req, true)
	}()
	return nil
}

func (r *BatchRunner) Stop(batchID int64) error {
	r.mu.Lock()
	cancel, ok := r.cancels[batchID]
	r.mu.Unlock()
	if !ok {
		return errors.New("batch is not running")
	}
	cancel()
	return nil
}

func (r *BatchRunner) executeWithContext(runCtx context.Context, batch model.Batch, req model.CreateBatchRequest, resume bool) {
	defer r.clearCancel(batch.ID)

	ctx := context.Background()
	now := r.now()
	if err := r.store.MarkBatchRunning(ctx, batch.ID, now); err != nil {
		log.Printf("mark batch running failed: %v", err)
		return
	}

	progress, err := r.loadBatchProgress(ctx, batch.ID)
	if err != nil {
		log.Printf("load batch progress failed: %v", err)
		_ = r.store.MarkBatchFailed(ctx, batch.ID, r.now(), 0, err.Error())
		return
	}

	var quotaUsed int
	var issues []string
	logf := func(level, stage, message string, payload any) {
		encoded := ""
		if payload != nil {
			data, _ := json.Marshal(payload)
			encoded = string(data)
		}
		event, err := r.store.AppendJobLog(ctx, batch.ID, level, stage, message, encoded, r.now())
		if err == nil {
			r.broker.Publish(batch.ID, event)
		}
		if level == "error" {
			issues = append(issues, message)
		}
	}

	stopBatch := func() {
		summary := strings.Join(uniqueStrings(append(issues, "stopped by user")), "; ")
		if err := r.store.MarkBatchStopped(ctx, batch.ID, r.now(), summary); err != nil {
			log.Printf("mark batch stopped failed: %v", err)
		}
		logf("warn", "batch", "batch stopped", map[string]any{"reason": "stopped by user"})
	}

	startMessage := "batch started"
	if resume {
		startMessage = "batch resumed"
	}
	logf("info", "batch", startMessage, map[string]any{
		"name":     batch.Name,
		"keywords": req.SeedKeywords,
	})

	settings, err := r.store.GetSettings(ctx)
	if err != nil {
		logf("error", "settings", "failed to load settings", map[string]any{"error": err.Error()})
		_ = r.store.MarkBatchFailed(ctx, batch.ID, r.now(), quotaUsed, err.Error())
		return
	}

	if runCtx.Err() != nil {
		stopBatch()
		return
	}

	for index, seed := range req.SeedKeywords {
		_ = r.store.SaveKeyword(ctx, batch.ID, model.KeywordRecord{Term: seed, Phase: "seed", Position: index})
	}

	if strings.TrimSpace(settings.YouTubeAPIKey) == "" {
		err := errors.New("youtube api key is empty in settings")
		logf("error", "youtube", err.Error(), nil)
		_ = r.store.MarkBatchFailed(ctx, batch.ID, r.now(), quotaUsed, err.Error())
		return
	}

	keywordLLM, youtubeClient, shortVerifier, err := r.resolveServices(settings)
	if err != nil {
		logf("error", "settings", "failed to build runtime services", map[string]any{"error": err.Error()})
		_ = r.store.MarkBatchFailed(ctx, batch.ID, r.now(), 0, err.Error())
		return
	}

	publishedAfter := r.now().AddDate(0, 0, -batch.PublishedWithinDays)
	videoToKeywords := progress.videoToKeywords
	if videoToKeywords == nil {
		videoToKeywords = map[string][]string{}
	}
	allIDs := append([]string(nil), progress.allCandidateIDs...)
	searchedTerms := progress.searchedTerms
	if searchedTerms == nil {
		searchedTerms = map[string]struct{}{}
	}
	searchTerms := func(terms []string, stage string) bool {
		for _, term := range terms {
			if runCtx.Err() != nil {
				return false
			}
			normalized := strings.TrimSpace(strings.ToLower(term))
			if normalized == "" {
				continue
			}
			if _, ok := searchedTerms[normalized]; ok {
				continue
			}
			logf("info", "youtube", "searching keyword", map[string]any{"keyword": term, "source": stage})
			hits, cost, err := youtubeClient.SearchVideos(runCtx, term, settings.DefaultMarket, publishedAfter, 50)
			quotaUsed += cost
			_ = r.store.IncrementBatchQuota(ctx, batch.ID, cost)
			if err != nil {
				if runCtx.Err() != nil {
					return false
				}
				logf("error", "youtube", "youtube search failed", map[string]any{"keyword": term, "source": stage, "error": err.Error()})
				continue
			}
			persisted := true
			for _, hit := range hits {
				if hit.VideoID == "" {
					continue
				}
				if err := r.store.AddSearchCandidate(ctx, batch.ID, hit.VideoID, term, r.now()); err != nil {
					persisted = false
					logf("error", "store", "failed to persist search candidate", map[string]any{"videoId": hit.VideoID, "keyword": term, "error": err.Error()})
					continue
				}
			}
			if persisted {
				searchedTerms[normalized] = struct{}{}
				if err := r.store.RecordSearchedTerm(ctx, batch.ID, term, stage, len(hits), r.now()); err != nil {
					logf("error", "store", "failed to persist searched term", map[string]any{"keyword": term, "source": stage, "error": err.Error()})
				}
			}
			logf("info", "youtube", "youtube search complete", map[string]any{"keyword": term, "source": stage, "hits": len(hits)})
			for _, hit := range hits {
				if hit.VideoID == "" {
					continue
				}
				videoToKeywords[hit.VideoID] = appendIfMissing(videoToKeywords[hit.VideoID], term)
				if !slices.Contains(allIDs, hit.VideoID) {
					allIDs = append(allIDs, hit.VideoID)
				}
			}
		}
		return true
	}

	if !searchTerms(req.SeedKeywords, "seed") {
		stopBatch()
		return
	}

	ruleKeywords := progress.ruleKeywords
	if len(ruleKeywords) == 0 {
		ruleKeywords = keywords.RuleExpand(req.SeedKeywords)
		for index, term := range ruleKeywords {
			_ = r.store.SaveKeyword(ctx, batch.ID, model.KeywordRecord{Term: term, Phase: "rule", Position: index})
		}
	}
	logf("info", "keywords", "rule keyword expansion complete", map[string]any{"count": len(ruleKeywords)})

	if runCtx.Err() != nil {
		stopBatch()
		return
	}

	providerResult := llm.ProviderResult{Keywords: append([]string(nil), progress.llmKeywords...)}
	if !progress.llmCompleted {
		llmPrompt := llm.BuildPrompt(req.SeedKeywords, ruleKeywords)
		logf("info", "llm", "starting free-web llm expansion", map[string]any{
			"providers": settings.ProviderOrder,
			"timeoutMs": 35000,
		})
		providerResult, err = keywordLLM.ExpandWithFallback(runCtx, settings.ProviderOrder, llmPrompt, func(provider, state, detail string) {
			switch state {
			case "start":
				logf("info", "llm", "trying provider", map[string]any{"provider": provider})
			case "failed":
				logf("warn", "llm", "provider failed", map[string]any{"provider": provider, "error": detail})
			}
		})
		if err != nil {
			if runCtx.Err() != nil {
				stopBatch()
				return
			}
			logf("warn", "llm", "all llm providers failed; continuing with rule keywords", map[string]any{"error": err.Error()})
			_ = r.store.SaveLLMRun(ctx, batch.ID, "none", llmPrompt, "", "failed", err.Error(), r.now(), r.now())
			providerResult = llm.ProviderResult{}
		} else {
			logf("info", "llm", "llm keyword expansion succeeded", map[string]any{"provider": providerResult.Provider, "count": len(providerResult.Keywords)})
			_ = r.store.SaveLLMRun(ctx, batch.ID, providerResult.Provider, llmPrompt, providerResult.Raw, "success", "", r.now(), r.now())
			for index, term := range providerResult.Keywords {
				_ = r.store.SaveKeyword(ctx, batch.ID, model.KeywordRecord{Term: term, Phase: "llm", Provider: providerResult.Provider, Position: index})
			}
		}
	} else {
		logf("info", "llm", "reusing saved llm expansion", map[string]any{"count": len(providerResult.Keywords)})
	}

	finalKeywords := progress.finalKeywords
	if len(finalKeywords) == 0 {
		finalKeywords = keywords.Cap(keywords.MergeUnique(req.SeedKeywords, ruleKeywords, providerResult.Keywords), batch.KeywordCap)
		for index, term := range finalKeywords {
			_ = r.store.SaveKeyword(ctx, batch.ID, model.KeywordRecord{Term: term, Phase: "final", Position: index})
		}
	}
	logf("info", "keywords", "final keyword list ready", map[string]any{"keywords": finalKeywords})

	if !searchTerms(finalKeywords, "expanded") {
		stopBatch()
		return
	}

	if len(allIDs) == 0 {
		logf("warn", "youtube", "no candidate videos found", nil)
		_ = r.store.MarkBatchCompleted(ctx, batch.ID, r.now(), 0, 0, strings.Join(issues, "; "))
		return
	}

	pendingIDs := make([]string, 0, len(allIDs))
	for _, videoID := range allIDs {
		if _, ok := progress.processedVideoIDs[videoID]; ok {
			continue
		}
		pendingIDs = append(pendingIDs, videoID)
	}

	detailsByID := map[string]model.VideoDetail{}
	for start := 0; start < len(pendingIDs); start += 50 {
		if runCtx.Err() != nil {
			stopBatch()
			return
		}
		end := start + 50
		if end > len(pendingIDs) {
			end = len(pendingIDs)
		}
		chunk := pendingIDs[start:end]
		details, cost, err := youtubeClient.VideoDetails(runCtx, chunk)
		quotaUsed += cost
		_ = r.store.IncrementBatchQuota(ctx, batch.ID, cost)
		if err != nil {
			if runCtx.Err() != nil {
				stopBatch()
				return
			}
			logf("error", "youtube", "youtube detail fetch failed", map[string]any{"error": err.Error()})
			continue
		}
		for _, detail := range details {
			detailsByID[detail.VideoID] = detail
		}
		logf("info", "youtube", "video detail progress", map[string]any{
			"processed": end,
			"total":     len(pendingIDs),
		})
	}

	now = r.now()
	verifiedCount := 0
	visibleCount := 0
	if len(pendingIDs) > 0 {
		logf("info", "verify", "verifying candidate videos", map[string]any{
			"processed": 0,
			"total":     len(pendingIDs),
		})
	}
	processedCount := 0
	for _, videoID := range pendingIDs {
		if runCtx.Err() != nil {
			stopBatch()
			return
		}
		detail, ok := detailsByID[videoID]
		if !ok || detail.DurationSec > 180 {
			processedCount++
			if processedCount == len(pendingIDs) || processedCount%10 == 0 {
				logf("info", "verify", "verification progress", map[string]any{
					"processed":      processedCount,
					"total":          len(pendingIDs),
					"verifiedResults": verifiedCount,
					"visibleResults":  visibleCount,
				})
			}
			continue
		}

		status, err := shortVerifier.Verify(runCtx, videoID)
		if err != nil {
			if runCtx.Err() != nil {
				stopBatch()
				return
			}
			logf("warn", "verify", "short verification fell back to unknown", map[string]any{"videoId": videoID, "error": err.Error()})
		}
		video := model.VideoRecord{
			VideoID:          detail.VideoID,
			WatchURL:         detail.WatchURL,
			ShortsURL:        detail.ShortsURL,
			Title:            detail.Title,
			ChannelTitle:     detail.ChannelTitle,
			PublishedAt:      detail.PublishedAt,
			DurationSec:      detail.DurationSec,
			Views:            detail.Views,
			VerifiedShort:    status,
			ThumbnailURL:     detail.ThumbnailURL,
			LastSeenAt:       now,
			FirstSeenBatchID: batch.ID,
		}
		existedBefore, err := r.store.UpsertVideo(ctx, video)
		if err != nil {
			logf("error", "store", "failed to upsert video", map[string]any{"videoId": videoID, "error": err.Error()})
			continue
		}

		metrics := ranking.Compute(now, detail.PublishedAt, detail.Views)
		for _, sourceKeyword := range videoToKeywords[videoID] {
			if err := r.store.AddBatchHit(ctx, model.BatchHit{
				BatchID:         batch.ID,
				VideoID:         videoID,
				SourceKeyword:   sourceKeyword,
				DuplicateSeen:   existedBefore,
				HiddenByDefault: req.HidePreviouslySeen && existedBefore,
				AgeDays:         metrics.AgeDays,
				ViewsPerDay:     metrics.ViewsPerDay,
				BreakoutScore:   metrics.BreakoutScore,
				CreatedAt:       now,
			}); err != nil {
				logf("error", "store", "failed to persist batch hit", map[string]any{"videoId": videoID, "error": err.Error()})
			}
		}
		progress.processedVideoIDs[videoID] = struct{}{}
		processedCount++
		if status != verifier.StatusFalse {
			visibleCount++
		}
		if status == verifier.StatusTrue {
			verifiedCount++
		}
		if processedCount == len(pendingIDs) || processedCount%10 == 0 {
			logf("info", "verify", "verification progress", map[string]any{
				"processed":       processedCount,
				"total":           len(pendingIDs),
				"verifiedResults": verifiedCount,
				"visibleResults":  visibleCount,
			})
		}
	}

	titles, err := r.store.ListTopTitles(ctx, batch.ID, 50)
	if err == nil {
		suggestions := keywords.ExtractSuggestions(titles, 10)
		for index, term := range suggestions {
			_ = r.store.SaveKeyword(ctx, batch.ID, model.KeywordRecord{Term: term, Phase: "suggested", Position: index})
		}
		logf("info", "suggest", "suggested keywords generated", map[string]any{"keywords": suggestions})
	}

	if runCtx.Err() != nil {
		stopBatch()
		return
	}

	if currentBatch, err := r.store.GetBatch(ctx, batch.ID); err == nil {
		visibleCount = currentBatch.TotalResults
	}
	errorSummary := strings.Join(uniqueStrings(issues), "; ")
	if err := r.store.MarkBatchCompleted(ctx, batch.ID, r.now(), visibleCount, 0, errorSummary); err != nil {
		log.Printf("mark batch completed failed: %v", err)
	}
	logf("info", "batch", "batch completed", map[string]any{
		"verifiedResults": verifiedCount,
		"visibleResults":  visibleCount,
		"quotaUsed":       quotaUsed,
	})
}

func (r *BatchRunner) loadBatchProgress(ctx context.Context, batchID int64) (batchProgress, error) {
	ruleKeywords, err := r.store.ListKeywordsByPhase(ctx, batchID, "rule")
	if err != nil {
		return batchProgress{}, err
	}
	llmKeywords, err := r.store.ListKeywordsByPhase(ctx, batchID, "llm")
	if err != nil {
		return batchProgress{}, err
	}
	finalKeywords, err := r.store.ListKeywordsByPhase(ctx, batchID, "final")
	if err != nil {
		return batchProgress{}, err
	}
	llmCompleted, err := r.store.HasLLMRun(ctx, batchID)
	if err != nil {
		return batchProgress{}, err
	}
	searchedList, err := r.store.ListSearchedTerms(ctx, batchID)
	if err != nil {
		return batchProgress{}, err
	}
	searchedTerms := make(map[string]struct{}, len(searchedList))
	for _, term := range searchedList {
		searchedTerms[term] = struct{}{}
	}
	videoToKeywords, allCandidateIDs, err := r.store.ListSearchCandidates(ctx, batchID)
	if err != nil {
		return batchProgress{}, err
	}
	processedVideoIDs, err := r.store.ListProcessedBatchVideoIDs(ctx, batchID)
	if err != nil {
		return batchProgress{}, err
	}
	if processedVideoIDs == nil {
		processedVideoIDs = map[string]struct{}{}
	}
	return batchProgress{
		ruleKeywords:      ruleKeywords,
		llmKeywords:       llmKeywords,
		finalKeywords:     finalKeywords,
		llmCompleted:      llmCompleted,
		searchedTerms:     searchedTerms,
		videoToKeywords:   videoToKeywords,
		allCandidateIDs:   allCandidateIDs,
		processedVideoIDs: processedVideoIDs,
	}, nil
}

func (r *BatchRunner) resolveServices(settings model.Settings) (keywordExpander, youtubeService, shortVerifier, error) {
	keywordLLM := r.llm
	youtubeClient := r.youtube
	shortVerifier := r.verifier
	if keywordLLM != nil && youtubeClient != nil && shortVerifier != nil {
		return keywordLLM, youtubeClient, shortVerifier, nil
	}

	resolvedProxyURL, err := netutil.ResolveProxyURL(strings.TrimSpace(settings.ProxyURL))
	if err != nil {
		return nil, nil, nil, err
	}
	httpClient, err := netutil.NewHTTPClient(resolvedProxyURL, 45*time.Second)
	if err != nil {
		return nil, nil, nil, err
	}
	if keywordLLM == nil {
		keywordLLM = llm.NewManager(resolvedProxyURL, r.profilesDir)
	}
	if youtubeClient == nil {
		apiKey := strings.TrimSpace(settings.YouTubeAPIKey)
		youtubeClient = youtube.NewClient(httpClient, func(context.Context) (string, error) {
			return apiKey, nil
		})
	}
	if shortVerifier == nil {
		shortVerifier = verifier.New(httpClient)
	}
	return keywordLLM, youtubeClient, shortVerifier, nil
}

func (r *BatchRunner) storeCancel(batchID int64, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancels == nil {
		r.cancels = map[int64]context.CancelFunc{}
	}
	r.cancels[batchID] = cancel
}

func (r *BatchRunner) clearCancel(batchID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.cancels, batchID)
}

func appendIfMissing(list []string, value string) []string {
	if slices.Contains(list, value) {
		return list
	}
	return append(list, value)
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
