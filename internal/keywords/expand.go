package keywords

import (
	"regexp"
	"slices"
	"strings"
)

var (
	spacePattern = regexp.MustCompile(`\s+`)
	nonWord      = regexp.MustCompile(`[^a-z0-9\s]+`)
	stopwords    = map[string]struct{}{
		"the": {}, "and": {}, "for": {}, "that": {}, "with": {}, "this": {}, "from": {}, "your": {},
		"have": {}, "just": {}, "into": {}, "about": {}, "they": {}, "them": {}, "were": {}, "what": {},
		"when": {}, "where": {}, "which": {}, "will": {}, "would": {}, "there": {}, "their": {}, "video": {},
		"shorts": {}, "short": {}, "youtube": {}, "viral": {}, "best": {}, "top": {}, "new": {},
	}
)

func Normalize(term string) string {
	term = strings.ToLower(strings.TrimSpace(term))
	term = nonWord.ReplaceAllString(term, " ")
	term = spacePattern.ReplaceAllString(term, " ")
	return strings.TrimSpace(term)
}

func MergeUnique(keywords ...[]string) []string {
	seen := map[string]string{}
	order := []string{}
	for _, list := range keywords {
		for _, item := range list {
			normalized := Normalize(item)
			if normalized == "" {
				continue
			}
			if _, ok := seen[normalized]; ok {
				continue
			}
			seen[normalized] = item
			order = append(order, normalized)
		}
	}
	out := make([]string, 0, len(order))
	for _, key := range order {
		out = append(out, seen[key])
	}
	return out
}

func RuleExpand(seeds []string) []string {
	templates := []string{
		"%s",
		"%s shorts",
		"%s viral shorts",
		"%s trending shorts",
		"best %s shorts",
		"%s compilation shorts",
		"%s challenge shorts",
		"%s clips shorts",
	}

	expanded := make([]string, 0, len(seeds)*len(templates))
	for _, seed := range seeds {
		base := Normalize(seed)
		if base == "" {
			continue
		}
		for _, template := range templates {
			expanded = append(expanded, strings.TrimSpace(strings.ReplaceAll(template, "%s", base)))
		}
	}
	return MergeUnique(expanded)
}

func Cap(list []string, limit int) []string {
	if limit <= 0 || len(list) <= limit {
		return slices.Clone(list)
	}
	return slices.Clone(list[:limit])
}

func ExtractSuggestions(titles []string, limit int) []string {
	if limit <= 0 {
		return nil
	}

	counts := map[string]int{}
	for _, title := range titles {
		tokens := tokenize(title)
		for n := 2; n <= 3; n++ {
			for i := 0; i+n <= len(tokens); i++ {
				gram := strings.Join(tokens[i:i+n], " ")
				if gram == "" {
					continue
				}
				counts[gram]++
			}
		}
	}

	type pair struct {
		key   string
		count int
	}
	pairs := make([]pair, 0, len(counts))
	for key, count := range counts {
		if count < 2 {
			continue
		}
		pairs = append(pairs, pair{key: key, count: count})
	}

	slices.SortFunc(pairs, func(a, b pair) int {
		if a.count == b.count {
			return strings.Compare(a.key, b.key)
		}
		if a.count > b.count {
			return -1
		}
		return 1
	})

	out := make([]string, 0, limit)
	for _, item := range pairs {
		out = append(out, item.key)
		if len(out) == limit {
			break
		}
	}
	return out
}

func tokenize(title string) []string {
	normalized := Normalize(title)
	if normalized == "" {
		return nil
	}
	parts := strings.Fields(normalized)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) < 3 {
			continue
		}
		if _, ok := stopwords[part]; ok {
			continue
		}
		out = append(out, part)
	}
	return out
}
