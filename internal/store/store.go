package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"youtubeshort/internal/model"
)

const initSQL = `
CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS batches (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  started_at TEXT,
  finished_at TEXT,
  published_within_days INTEGER NOT NULL,
  keyword_cap INTEGER NOT NULL,
  hide_previously_seen INTEGER NOT NULL,
  search_quota_units INTEGER NOT NULL DEFAULT 0,
  error_summary TEXT NOT NULL DEFAULT '',
  total_results INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS keywords (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  batch_id INTEGER NOT NULL,
  term TEXT NOT NULL,
  normalized_term TEXT NOT NULL,
  phase TEXT NOT NULL,
  provider TEXT NOT NULL DEFAULT '',
  position INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  UNIQUE(batch_id, normalized_term, phase, provider)
);

CREATE TABLE IF NOT EXISTS videos (
  video_id TEXT PRIMARY KEY,
  watch_url TEXT NOT NULL,
  shorts_url TEXT NOT NULL,
  title TEXT NOT NULL,
  channel_title TEXT NOT NULL,
  published_at TEXT NOT NULL,
  duration_sec INTEGER NOT NULL,
  views INTEGER NOT NULL,
  verified_short TEXT NOT NULL,
  thumbnail_url TEXT NOT NULL,
  last_seen_at TEXT NOT NULL,
  first_seen_batch_id INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS batch_hits (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  batch_id INTEGER NOT NULL,
  video_id TEXT NOT NULL,
  source_keyword TEXT NOT NULL,
  duplicate_seen INTEGER NOT NULL,
  hidden_by_default INTEGER NOT NULL,
  age_days INTEGER NOT NULL,
  views_per_day REAL NOT NULL,
  breakout_score REAL NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(batch_id, video_id, source_keyword)
);

CREATE TABLE IF NOT EXISTS searched_terms (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  batch_id INTEGER NOT NULL,
  term TEXT NOT NULL,
  normalized_term TEXT NOT NULL,
  source TEXT NOT NULL DEFAULT '',
  hits_count INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  UNIQUE(batch_id, normalized_term)
);

CREATE TABLE IF NOT EXISTS search_candidates (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  batch_id INTEGER NOT NULL,
  video_id TEXT NOT NULL,
  source_keyword TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(batch_id, video_id, source_keyword)
);

CREATE TABLE IF NOT EXISTS llm_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  batch_id INTEGER NOT NULL,
  provider TEXT NOT NULL,
  prompt TEXT NOT NULL,
  raw_response TEXT NOT NULL,
  status TEXT NOT NULL,
  error_message TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  finished_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS job_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  batch_id INTEGER NOT NULL,
  sequence INTEGER NOT NULL,
  level TEXT NOT NULL,
  stage TEXT NOT NULL,
  message TEXT NOT NULL,
  payload TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_keywords_batch_phase ON keywords(batch_id, phase, position);
CREATE INDEX IF NOT EXISTS idx_hits_batch ON batch_hits(batch_id);
CREATE INDEX IF NOT EXISTS idx_searched_terms_batch ON searched_terms(batch_id);
CREATE INDEX IF NOT EXISTS idx_search_candidates_batch ON search_candidates(batch_id);
CREATE INDEX IF NOT EXISTS idx_logs_batch_seq ON job_logs(batch_id, sequence);
`

type Store struct {
	db *sql.DB
}

func New(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	store := &Store{db: db}
	if err := store.Migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.ensureDefaultSettings(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, initSQL)
	return err
}

func (s *Store) ensureDefaultSettings(ctx context.Context) error {
	settings, err := s.GetSettings(ctx)
	if err != nil {
		return err
	}
	if len(settings.ProviderOrder) == 0 {
		settings.ProviderOrder = []string{"chatgpt", "gemini", "copilot", "perplexity"}
	}
	if settings.DefaultMarket == "" {
		settings.DefaultMarket = "en"
	}
	return s.UpdateSettings(ctx, settings)
}

func (s *Store) GetSettings(ctx context.Context) (model.Settings, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return model.Settings{}, err
	}
	defer rows.Close()

	settings := model.Settings{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return model.Settings{}, err
		}
		switch key {
		case "youtube_api_key":
			settings.YouTubeAPIKey = value
		case "proxy_url":
			settings.ProxyURL = value
		case "provider_order":
			if value != "" {
				if err := json.Unmarshal([]byte(value), &settings.ProviderOrder); err != nil {
					return model.Settings{}, err
				}
			}
		case "default_market":
			settings.DefaultMarket = value
		}
	}
	return settings, rows.Err()
}

func (s *Store) UpdateSettings(ctx context.Context, settings model.Settings) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)

	providers, err := json.Marshal(settings.ProviderOrder)
	if err != nil {
		return err
	}

	items := map[string]string{
		"youtube_api_key": settings.YouTubeAPIKey,
		"proxy_url":       settings.ProxyURL,
		"provider_order":  string(providers),
		"default_market":  settings.DefaultMarket,
	}

	for key, value := range items {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO settings(key, value) VALUES(?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value
		`, key, value); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *Store) CreateBatch(ctx context.Context, req model.CreateBatchRequest, now time.Time) (model.Batch, error) {
	if req.PublishedWithinDays <= 0 {
		req.PublishedWithinDays = 90
	}
	if req.KeywordCap <= 0 {
		req.KeywordCap = 15
	}
	if strings.TrimSpace(req.Name) == "" {
		req.Name = fmt.Sprintf("Batch %s", now.Format("2006-01-02 15:04:05"))
	}

	result, err := s.db.ExecContext(ctx, `
		INSERT INTO batches(name, status, created_at, published_within_days, keyword_cap, hide_previously_seen)
		VALUES(?, 'queued', ?, ?, ?, ?)
	`, req.Name, now.UTC().Format(time.RFC3339), req.PublishedWithinDays, req.KeywordCap, boolToInt(req.HidePreviouslySeen))
	if err != nil {
		return model.Batch{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return model.Batch{}, err
	}
	return s.GetBatch(ctx, id)
}

func (s *Store) GetBatch(ctx context.Context, batchID int64) (model.Batch, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT b.id, b.name, b.status, b.created_at, b.started_at, b.finished_at, b.published_within_days, b.keyword_cap,
		       b.hide_previously_seen, b.search_quota_units, b.error_summary,
		       (
		         SELECT COUNT(DISTINCT v.video_id)
		         FROM batch_hits h
		         JOIN videos v ON v.video_id = h.video_id
		         WHERE h.batch_id = b.id AND v.verified_short IN ('true', 'unknown')
		       ) AS total_results
		FROM batches b
		WHERE b.id = ?
	`, batchID)
	var batch model.Batch
	var createdAt string
	var startedAt, finishedAt sql.NullString
	var hideSeen int
	if err := row.Scan(
		&batch.ID,
		&batch.Name,
		&batch.Status,
		&createdAt,
		&startedAt,
		&finishedAt,
		&batch.PublishedWithinDays,
		&batch.KeywordCap,
		&hideSeen,
		&batch.SearchQuotaUnits,
		&batch.ErrorSummary,
		&batch.TotalResults,
	); err != nil {
		return model.Batch{}, err
	}

	created, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return model.Batch{}, err
	}
	batch.CreatedAt = created
	if startedAt.Valid {
		value, err := time.Parse(time.RFC3339, startedAt.String)
		if err != nil {
			return model.Batch{}, err
		}
		batch.StartedAt = &value
	}
	if finishedAt.Valid {
		value, err := time.Parse(time.RFC3339, finishedAt.String)
		if err != nil {
			return model.Batch{}, err
		}
		batch.FinishedAt = &value
	}
	batch.HidePreviouslySeen = hideSeen == 1
	seeds, err := s.ListKeywordsByPhase(ctx, batchID, "seed")
	if err != nil {
		return model.Batch{}, err
	}
	batch.SeedKeywords = seeds
	suggestions, err := s.ListKeywordsByPhase(ctx, batchID, "suggested")
	if err != nil {
		return model.Batch{}, err
	}
	batch.SuggestedKeywords = suggestions
	return batch, nil
}

func (s *Store) ListBatches(ctx context.Context, limit int) ([]model.Batch, error) {
	if limit <= 0 {
		limit = 20
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT b.id, b.name, b.status, b.created_at, b.started_at, b.finished_at, b.published_within_days, b.keyword_cap,
		       b.hide_previously_seen, b.search_quota_units, b.error_summary,
		       (
		         SELECT COUNT(DISTINCT v.video_id)
		         FROM batch_hits h
		         JOIN videos v ON v.video_id = h.video_id
		         WHERE h.batch_id = b.id AND v.verified_short IN ('true', 'unknown')
		       ) AS total_results
		FROM batches b
		ORDER BY id DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	batches := make([]model.Batch, 0, limit)
	for rows.Next() {
		batch, err := scanBatchRow(rows)
		if err != nil {
			return nil, err
		}
		batches = append(batches, batch)
	}
	return batches, rows.Err()
}

func (s *Store) MarkBatchRunning(ctx context.Context, batchID int64, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE batches
		SET status = 'running',
		    started_at = COALESCE(started_at, ?),
		    finished_at = NULL,
		    error_summary = ''
		WHERE id = ?
	`, now.UTC().Format(time.RFC3339), batchID)
	return err
}

func (s *Store) PrepareBatchResume(ctx context.Context, batchID int64) error {
	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM batches WHERE id = ?`, batchID).Scan(&status); err != nil {
		return err
	}
	if status != "stopped" && status != "failed" {
		return fmt.Errorf("cannot resume batch with status %q", status)
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE batches
		SET status = 'queued',
		    finished_at = NULL
		WHERE id = ?
	`, batchID)
	return err
}

func (s *Store) DeleteBatch(ctx context.Context, batchID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)

	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM batches WHERE id = ?`, batchID).Scan(&status); err != nil {
		return err
	}
	if status == "running" || status == "queued" {
		return errors.New("cannot delete running batch")
	}

	for _, query := range []string{
		`DELETE FROM batch_hits WHERE batch_id = ?`,
		`DELETE FROM keywords WHERE batch_id = ?`,
		`DELETE FROM llm_runs WHERE batch_id = ?`,
		`DELETE FROM job_logs WHERE batch_id = ?`,
		`DELETE FROM batches WHERE id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, query, batchID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM videos WHERE video_id NOT IN (SELECT DISTINCT video_id FROM batch_hits)`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MarkBatchCompleted(ctx context.Context, batchID int64, now time.Time, totalResults, quotaUnits int, errorSummary string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE batches
		SET status = 'completed', finished_at = ?, total_results = ?, search_quota_units = search_quota_units + ?, error_summary = ?
		WHERE id = ?
	`, now.UTC().Format(time.RFC3339), totalResults, quotaUnits, errorSummary, batchID)
	return err
}

func (s *Store) MarkBatchFailed(ctx context.Context, batchID int64, now time.Time, quotaUnits int, errorSummary string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE batches
		SET status = 'failed', finished_at = ?, search_quota_units = search_quota_units + ?, error_summary = ?
		WHERE id = ?
	`, now.UTC().Format(time.RFC3339), quotaUnits, errorSummary, batchID)
	return err
}

func (s *Store) MarkBatchStopped(ctx context.Context, batchID int64, now time.Time, errorSummary string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE batches
		SET status = 'stopped', finished_at = ?, error_summary = ?
		WHERE id = ?
	`, now.UTC().Format(time.RFC3339), errorSummary, batchID)
	return err
}

func (s *Store) RecoverInterruptedBatches(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE batches
		SET status = 'failed',
		    finished_at = ?,
		    error_summary = CASE
		      WHEN TRIM(error_summary) = '' THEN 'batch interrupted by app restart'
		      ELSE error_summary || '; batch interrupted by app restart'
		    END
		WHERE status IN ('queued', 'running') AND finished_at IS NULL
	`, now.UTC().Format(time.RFC3339))
	return err
}

func (s *Store) IncrementBatchQuota(ctx context.Context, batchID int64, delta int) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE batches SET search_quota_units = search_quota_units + ? WHERE id = ?
	`, delta, batchID)
	return err
}

func (s *Store) SaveKeyword(ctx context.Context, batchID int64, record model.KeywordRecord) error {
	if strings.TrimSpace(record.Term) == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO keywords(batch_id, term, normalized_term, phase, provider, position, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)
	`, batchID, record.Term, normalize(record.Term), record.Phase, record.Provider, record.Position, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) RecordSearchedTerm(ctx context.Context, batchID int64, term, source string, hitsCount int, now time.Time) error {
	if strings.TrimSpace(term) == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO searched_terms(batch_id, term, normalized_term, source, hits_count, created_at)
		VALUES(?, ?, ?, ?, ?, ?)
	`, batchID, term, normalize(term), source, hitsCount, now.UTC().Format(time.RFC3339))
	return err
}

func (s *Store) ListSearchedTerms(ctx context.Context, batchID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT normalized_term
		FROM searched_terms
		WHERE batch_id = ?
		ORDER BY id ASC
	`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]string, 0)
	for rows.Next() {
		var term string
		if err := rows.Scan(&term); err != nil {
			return nil, err
		}
		out = append(out, term)
	}
	return out, rows.Err()
}

func (s *Store) AddSearchCandidate(ctx context.Context, batchID int64, videoID, sourceKeyword string, now time.Time) error {
	if strings.TrimSpace(videoID) == "" || strings.TrimSpace(sourceKeyword) == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO search_candidates(batch_id, video_id, source_keyword, created_at)
		VALUES(?, ?, ?, ?)
	`, batchID, videoID, sourceKeyword, now.UTC().Format(time.RFC3339))
	return err
}

func (s *Store) ListSearchCandidates(ctx context.Context, batchID int64) (map[string][]string, []string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT video_id, source_keyword
		FROM search_candidates
		WHERE batch_id = ?
		ORDER BY id ASC
	`, batchID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	videoToKeywords := map[string][]string{}
	allIDs := make([]string, 0)
	seen := map[string]struct{}{}
	for rows.Next() {
		var videoID, sourceKeyword string
		if err := rows.Scan(&videoID, &sourceKeyword); err != nil {
			return nil, nil, err
		}
		videoToKeywords[videoID] = appendIfMissing(videoToKeywords[videoID], sourceKeyword)
		if _, ok := seen[videoID]; ok {
			continue
		}
		seen[videoID] = struct{}{}
		allIDs = append(allIDs, videoID)
	}
	return videoToKeywords, allIDs, rows.Err()
}

func (s *Store) ListKeywordsByPhase(ctx context.Context, batchID int64, phase string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT term FROM keywords
		WHERE batch_id = ? AND phase = ?
		ORDER BY position ASC, id ASC
	`, batchID, phase)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]string, 0)
	for rows.Next() {
		var term string
		if err := rows.Scan(&term); err != nil {
			return nil, err
		}
		out = append(out, term)
	}
	return out, rows.Err()
}

func (s *Store) SaveLLMRun(ctx context.Context, batchID int64, provider, prompt, rawResponse, status, errorMessage string, startedAt, finishedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO llm_runs(batch_id, provider, prompt, raw_response, status, error_message, created_at, finished_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
	`, batchID, provider, prompt, rawResponse, status, errorMessage, startedAt.UTC().Format(time.RFC3339), finishedAt.UTC().Format(time.RFC3339))
	return err
}

func (s *Store) HasLLMRun(ctx context.Context, batchID int64) (bool, error) {
	var exists int
	if err := s.db.QueryRowContext(ctx, `
		SELECT CASE WHEN EXISTS(SELECT 1 FROM llm_runs WHERE batch_id = ?) THEN 1 ELSE 0 END
	`, batchID).Scan(&exists); err != nil {
		return false, err
	}
	return exists == 1, nil
}

func (s *Store) AppendJobLog(ctx context.Context, batchID int64, level, stage, message, payload string, now time.Time) (model.JobEvent, error) {
	sequence, err := s.nextSequence(ctx, batchID)
	if err != nil {
		return model.JobEvent{}, err
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO job_logs(batch_id, sequence, level, stage, message, payload, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)
	`, batchID, sequence, level, stage, message, payload, now.UTC().Format(time.RFC3339))
	if err != nil {
		return model.JobEvent{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return model.JobEvent{}, err
	}
	return model.JobEvent{
		ID:        id,
		BatchID:   batchID,
		Sequence:  sequence,
		Level:     level,
		Stage:     stage,
		Message:   message,
		Payload:   payload,
		CreatedAt: now.UTC(),
	}, nil
}

func (s *Store) ListJobLogs(ctx context.Context, batchID int64) ([]model.JobEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, batch_id, sequence, level, stage, message, payload, created_at
		FROM job_logs
		WHERE batch_id = ?
		ORDER BY sequence ASC
	`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]model.JobEvent, 0)
	for rows.Next() {
		var item model.JobEvent
		var createdAt string
		if err := rows.Scan(&item.ID, &item.BatchID, &item.Sequence, &item.Level, &item.Stage, &item.Message, &item.Payload, &createdAt); err != nil {
			return nil, err
		}
		timestamp, err := time.Parse(time.RFC3339, createdAt)
		if err != nil {
			return nil, err
		}
		item.CreatedAt = timestamp
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) UpsertVideo(ctx context.Context, video model.VideoRecord) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer rollback(tx)

	var existingBatchID int64
	err = tx.QueryRowContext(ctx, `SELECT first_seen_batch_id FROM videos WHERE video_id = ?`, video.VideoID).Scan(&existingBatchID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	existedBefore := err == nil && existingBatchID != video.FirstSeenBatchID

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO videos(video_id, watch_url, shorts_url, title, channel_title, published_at, duration_sec, views,
		                   verified_short, thumbnail_url, last_seen_at, first_seen_batch_id)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(video_id) DO UPDATE SET
		  watch_url = excluded.watch_url,
		  shorts_url = excluded.shorts_url,
		  title = excluded.title,
		  channel_title = excluded.channel_title,
		  published_at = excluded.published_at,
		  duration_sec = excluded.duration_sec,
		  views = excluded.views,
		  verified_short = excluded.verified_short,
		  thumbnail_url = excluded.thumbnail_url,
		  last_seen_at = excluded.last_seen_at
	`, video.VideoID, video.WatchURL, video.ShortsURL, video.Title, video.ChannelTitle, video.PublishedAt.UTC().Format(time.RFC3339),
		video.DurationSec, video.Views, video.VerifiedShort, video.ThumbnailURL, video.LastSeenAt.UTC().Format(time.RFC3339), video.FirstSeenBatchID); err != nil {
		return false, err
	}

	return existedBefore, tx.Commit()
}

func (s *Store) AddBatchHit(ctx context.Context, hit model.BatchHit) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO batch_hits(batch_id, video_id, source_keyword, duplicate_seen, hidden_by_default, age_days,
		                                 views_per_day, breakout_score, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, hit.BatchID, hit.VideoID, hit.SourceKeyword, boolToInt(hit.DuplicateSeen), boolToInt(hit.HiddenByDefault),
		hit.AgeDays, hit.ViewsPerDay, hit.BreakoutScore, hit.CreatedAt.UTC().Format(time.RFC3339))
	return err
}

func (s *Store) ListProcessedBatchVideoIDs(ctx context.Context, batchID int64) (map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT video_id
		FROM batch_hits
		WHERE batch_id = ?
	`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]struct{}{}
	for rows.Next() {
		var videoID string
		if err := rows.Scan(&videoID); err != nil {
			return nil, err
		}
		out[videoID] = struct{}{}
	}
	return out, rows.Err()
}

func (s *Store) ListBatchResults(ctx context.Context, batchID int64, sortBy string) ([]model.BatchResult, error) {
	orderBy := "breakout_score DESC, views DESC"
	switch sortBy {
	case "views":
		orderBy = "views DESC, breakout_score DESC"
	case "views_per_day":
		orderBy = "views_per_day DESC, views DESC"
	}

	query := `
		SELECT
			v.video_id,
			v.thumbnail_url,
			v.title,
			v.channel_title,
			v.published_at,
			v.views,
			MAX(h.views_per_day) AS views_per_day,
			MAX(h.breakout_score) AS breakout_score,
			GROUP_CONCAT(DISTINCT h.source_keyword) AS source_keywords,
			v.watch_url,
			v.shorts_url,
			MAX(h.duplicate_seen) AS duplicate_seen,
			MAX(h.hidden_by_default) AS hidden_by_default,
			v.verified_short
		FROM batch_hits h
		JOIN videos v ON v.video_id = h.video_id
		WHERE h.batch_id = ? AND v.verified_short IN ('true', 'unknown')
		GROUP BY v.video_id, v.thumbnail_url, v.title, v.channel_title, v.published_at, v.views, v.watch_url, v.shorts_url, v.verified_short
		ORDER BY CASE v.verified_short WHEN 'true' THEN 0 WHEN 'unknown' THEN 1 ELSE 2 END, ` + orderBy

	rows, err := s.db.QueryContext(ctx, query, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]model.BatchResult, 0)
	for rows.Next() {
		var item model.BatchResult
		var publishedAt string
		var duplicateSeen, hiddenByDefault int
		if err := rows.Scan(
			&item.VideoID,
			&item.Thumbnail,
			&item.Title,
			&item.Channel,
			&publishedAt,
			&item.Views,
			&item.ViewsPerDay,
			&item.BreakoutScore,
			&item.SourceKeyword,
			&item.WatchLink,
			&item.ShortsLink,
			&duplicateSeen,
			&hiddenByDefault,
			&item.VerifiedShort,
		); err != nil {
			return nil, err
		}
		item.DuplicateSeen = duplicateSeen == 1
		item.HiddenByDefault = hiddenByDefault == 1
		parsed, err := time.Parse(time.RFC3339, publishedAt)
		if err != nil {
			return nil, err
		}
		item.PublishedAt = parsed
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListTopTitles(ctx context.Context, batchID int64, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT v.title
		FROM batch_hits h
		JOIN videos v ON v.video_id = h.video_id
		WHERE h.batch_id = ? AND v.verified_short IN ('true', 'unknown')
		GROUP BY v.video_id, v.title
		ORDER BY MAX(CASE WHEN v.verified_short = 'true' THEN 1 ELSE 0 END) DESC, MAX(h.breakout_score) DESC
		LIMIT ?
	`, batchID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]string, 0)
	for rows.Next() {
		var title string
		if err := rows.Scan(&title); err != nil {
			return nil, err
		}
		out = append(out, title)
	}
	return out, rows.Err()
}

func appendIfMissing(list []string, value string) []string {
	for _, item := range list {
		if item == value {
			return list
		}
	}
	return append(list, value)
}

func (s *Store) CountVideos(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM videos`).Scan(&count)
	return count, err
}

func (s *Store) CountBatchHits(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM batch_hits`).Scan(&count)
	return count, err
}

type batchRowScanner interface {
	Scan(dest ...any) error
}

func scanBatchRow(row batchRowScanner) (model.Batch, error) {
	var batch model.Batch
	var createdAt string
	var startedAt, finishedAt sql.NullString
	var hideSeen int
	if err := row.Scan(
		&batch.ID,
		&batch.Name,
		&batch.Status,
		&createdAt,
		&startedAt,
		&finishedAt,
		&batch.PublishedWithinDays,
		&batch.KeywordCap,
		&hideSeen,
		&batch.SearchQuotaUnits,
		&batch.ErrorSummary,
		&batch.TotalResults,
	); err != nil {
		return model.Batch{}, err
	}

	created, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return model.Batch{}, err
	}
	batch.CreatedAt = created
	if startedAt.Valid {
		value, err := time.Parse(time.RFC3339, startedAt.String)
		if err != nil {
			return model.Batch{}, err
		}
		batch.StartedAt = &value
	}
	if finishedAt.Valid {
		value, err := time.Parse(time.RFC3339, finishedAt.String)
		if err != nil {
			return model.Batch{}, err
		}
		batch.FinishedAt = &value
	}
	batch.HidePreviouslySeen = hideSeen == 1
	return batch, nil
}

func rollback(tx *sql.Tx) {
	if tx != nil {
		_ = tx.Rollback()
	}
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func normalize(term string) string {
	term = strings.ToLower(strings.TrimSpace(term))
	term = strings.Join(strings.Fields(term), " ")
	return term
}

func (s *Store) nextSequence(ctx context.Context, batchID int64) (int, error) {
	var sequence int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1 FROM job_logs WHERE batch_id = ?`, batchID).Scan(&sequence)
	return sequence, err
}
