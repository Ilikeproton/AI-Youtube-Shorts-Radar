package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"youtubeshort/internal/model"
)

func TestRecoverInterruptedBatches(t *testing.T) {
	st, err := New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer st.Close()

	now := time.Date(2026, 3, 11, 9, 0, 0, 0, time.UTC)
	batch, err := st.CreateBatch(context.Background(), model.CreateBatchRequest{
		Name:                "Interrupted",
		SeedKeywords:        []string{"bmx tricks"},
		PublishedWithinDays: 90,
		KeywordCap:          15,
		HidePreviouslySeen:  true,
	}, now)
	if err != nil {
		t.Fatalf("CreateBatch failed: %v", err)
	}
	if err := st.MarkBatchRunning(context.Background(), batch.ID, now); err != nil {
		t.Fatalf("MarkBatchRunning failed: %v", err)
	}

	recoveredAt := now.Add(2 * time.Minute)
	if err := st.RecoverInterruptedBatches(context.Background(), recoveredAt); err != nil {
		t.Fatalf("RecoverInterruptedBatches failed: %v", err)
	}

	batch, err = st.GetBatch(context.Background(), batch.ID)
	if err != nil {
		t.Fatalf("GetBatch failed: %v", err)
	}
	if batch.Status != "failed" {
		t.Fatalf("expected batch status failed, got %q", batch.Status)
	}
	if batch.FinishedAt == nil {
		t.Fatal("expected finished_at to be set")
	}
	if !strings.Contains(batch.ErrorSummary, "app restart") {
		t.Fatalf("expected restart message in error summary, got %q", batch.ErrorSummary)
	}
}

func TestGetBatchIncludesSeedKeywords(t *testing.T) {
	st, err := New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer st.Close()

	now := time.Date(2026, 3, 11, 9, 0, 0, 0, time.UTC)
	batch, err := st.CreateBatch(context.Background(), model.CreateBatchRequest{
		Name:                "Seeds",
		SeedKeywords:        []string{"bmx tricks", "bike design"},
		PublishedWithinDays: 90,
		KeywordCap:          15,
		HidePreviouslySeen:  true,
	}, now)
	if err != nil {
		t.Fatalf("CreateBatch failed: %v", err)
	}
	for index, seed := range []string{"bmx tricks", "bike design"} {
		if err := st.SaveKeyword(context.Background(), batch.ID, model.KeywordRecord{
			Term:     seed,
			Phase:    "seed",
			Position: index,
		}); err != nil {
			t.Fatalf("SaveKeyword failed: %v", err)
		}
	}

	batch, err = st.GetBatch(context.Background(), batch.ID)
	if err != nil {
		t.Fatalf("GetBatch failed: %v", err)
	}
	if strings.Join(batch.SeedKeywords, ",") != "bmx tricks,bike design" {
		t.Fatalf("unexpected seed keywords: %#v", batch.SeedKeywords)
	}
}

func TestListBatchResultsIncludesUnknownShorts(t *testing.T) {
	st, err := New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer st.Close()

	now := time.Date(2026, 3, 11, 9, 0, 0, 0, time.UTC)
	batch, err := st.CreateBatch(context.Background(), model.CreateBatchRequest{
		Name:                "Unknown Results",
		SeedKeywords:        []string{"bmx tricks"},
		PublishedWithinDays: 90,
		KeywordCap:          15,
		HidePreviouslySeen:  true,
	}, now)
	if err != nil {
		t.Fatalf("CreateBatch failed: %v", err)
	}

	if _, err := st.db.ExecContext(context.Background(), `
		INSERT INTO videos(video_id, watch_url, shorts_url, title, channel_title, published_at, duration_sec, views,
		                   verified_short, thumbnail_url, last_seen_at, first_seen_batch_id)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "abc123", "https://youtube.com/watch?v=abc123", "https://youtube.com/shorts/abc123", "Unknown Short",
		"Creator", now.UTC().Format(time.RFC3339), 60, 120000, "unknown", "https://example.com/thumb.jpg",
		now.UTC().Format(time.RFC3339), batch.ID); err != nil {
		t.Fatalf("insert video failed: %v", err)
	}

	if err := st.AddBatchHit(context.Background(), model.BatchHit{
		BatchID:         batch.ID,
		VideoID:         "abc123",
		SourceKeyword:   "bmx tricks",
		DuplicateSeen:   false,
		HiddenByDefault: false,
		AgeDays:         2,
		ViewsPerDay:     60000,
		BreakoutScore:   10.5,
		CreatedAt:       now,
	}); err != nil {
		t.Fatalf("AddBatchHit failed: %v", err)
	}

	results, err := st.ListBatchResults(context.Background(), batch.ID, "score")
	if err != nil {
		t.Fatalf("ListBatchResults failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].VerifiedShort != "unknown" {
		t.Fatalf("expected unknown verified_short, got %q", results[0].VerifiedShort)
	}
}

func TestListBatchesUsesVisibleResultsCount(t *testing.T) {
	st, err := New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer st.Close()

	now := time.Date(2026, 3, 11, 9, 0, 0, 0, time.UTC)
	batch, err := st.CreateBatch(context.Background(), model.CreateBatchRequest{
		Name:                "Visible Count",
		SeedKeywords:        []string{"bmx tricks"},
		PublishedWithinDays: 90,
		KeywordCap:          15,
		HidePreviouslySeen:  true,
	}, now)
	if err != nil {
		t.Fatalf("CreateBatch failed: %v", err)
	}

	if _, err := st.db.ExecContext(context.Background(), `
		INSERT INTO videos(video_id, watch_url, shorts_url, title, channel_title, published_at, duration_sec, views,
		                   verified_short, thumbnail_url, last_seen_at, first_seen_batch_id)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "abc123", "https://youtube.com/watch?v=abc123", "https://youtube.com/shorts/abc123", "Visible",
		"Creator", now.UTC().Format(time.RFC3339), 60, 120000, "unknown", "https://example.com/thumb.jpg",
		now.UTC().Format(time.RFC3339), batch.ID); err != nil {
		t.Fatalf("insert video failed: %v", err)
	}
	if err := st.AddBatchHit(context.Background(), model.BatchHit{
		BatchID:         batch.ID,
		VideoID:         "abc123",
		SourceKeyword:   "bmx tricks",
		DuplicateSeen:   false,
		HiddenByDefault: false,
		AgeDays:         2,
		ViewsPerDay:     60000,
		BreakoutScore:   10.5,
		CreatedAt:       now,
	}); err != nil {
		t.Fatalf("AddBatchHit failed: %v", err)
	}

	batches, err := st.ListBatches(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListBatches failed: %v", err)
	}
	if len(batches) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(batches))
	}
	if batches[0].TotalResults != 1 {
		t.Fatalf("expected totalResults 1, got %d", batches[0].TotalResults)
	}
}

func TestDeleteBatchRemovesEmptyBatch(t *testing.T) {
	st, err := New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer st.Close()

	now := time.Date(2026, 3, 11, 9, 0, 0, 0, time.UTC)
	batch, err := st.CreateBatch(context.Background(), model.CreateBatchRequest{
		Name:                "Delete Me",
		SeedKeywords:        []string{"bmx tricks"},
		PublishedWithinDays: 90,
		KeywordCap:          15,
		HidePreviouslySeen:  true,
	}, now)
	if err != nil {
		t.Fatalf("CreateBatch failed: %v", err)
	}
	if err := st.MarkBatchFailed(context.Background(), batch.ID, now.Add(time.Minute), 0, "empty batch"); err != nil {
		t.Fatalf("MarkBatchFailed failed: %v", err)
	}

	if err := st.DeleteBatch(context.Background(), batch.ID); err != nil {
		t.Fatalf("DeleteBatch failed: %v", err)
	}

	batches, err := st.ListBatches(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListBatches failed: %v", err)
	}
	if len(batches) != 0 {
		t.Fatalf("expected no batches after delete, got %d", len(batches))
	}
}
