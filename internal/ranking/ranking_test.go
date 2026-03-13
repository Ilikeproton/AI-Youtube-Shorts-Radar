package ranking

import (
	"testing"
	"time"
)

func TestCompute(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	publishedAt := now.Add(-48 * time.Hour)
	metrics := Compute(now, publishedAt, 12000)
	if metrics.AgeDays != 2 {
		t.Fatalf("expected ageDays 2, got %d", metrics.AgeDays)
	}
	if metrics.ViewsPerDay != 6000 {
		t.Fatalf("expected viewsPerDay 6000, got %f", metrics.ViewsPerDay)
	}
	if metrics.BreakoutScore <= 0 {
		t.Fatalf("expected positive score, got %f", metrics.BreakoutScore)
	}
}
