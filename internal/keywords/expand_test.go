package keywords

import "testing"

func TestNormalizeAndMergeUnique(t *testing.T) {
	t.Parallel()

	values := MergeUnique([]string{"  Soccer  Highlights!! ", "soccer highlights"}, []string{"Cooking Hacks"})
	if len(values) != 2 {
		t.Fatalf("expected 2 unique values, got %d", len(values))
	}
	if Normalize(values[0]) != "soccer highlights" {
		t.Fatalf("unexpected normalized value %q", values[0])
	}
}

func TestExtractSuggestions(t *testing.T) {
	t.Parallel()

	titles := []string{
		"Wild soccer dribble challenge shorts",
		"Soccer dribble challenge in rain",
		"Street soccer dribble challenge reaction",
		"Cooking hacks for dinner",
	}
	suggestions := ExtractSuggestions(titles, 5)
	if len(suggestions) == 0 {
		t.Fatal("expected suggestions")
	}
	found := false
	for _, item := range suggestions {
		if item == "soccer dribble" || item == "dribble challenge" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected dribble suggestion, got %#v", suggestions)
	}
}
