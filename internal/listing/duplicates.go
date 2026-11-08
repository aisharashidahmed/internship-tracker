package listing

import (
	"sort"
	"strings"
)

// Candidate is a stored listing, as the possible-duplicates report sees it.
type Candidate struct {
	ID      int64
	Company string
	Title   string
	Season  string
}

// DuplicatePair is two listings that were kept apart but look alike.
type DuplicatePair struct {
	A, B       Candidate
	Similarity float64 // 0..1, share of title words in common
}

// PossibleDuplicates finds pairs at the same company and season whose titles
// share at least minSimilarity of their words (after normalization), but
// were not merged. It is a report for a human: matching stays strict, and
// this shows what that strictness missed, so rules can be improved.
func PossibleDuplicates(listings []Candidate, minSimilarity float64) []DuplicatePair {
	// Only listings at the same company and season can be duplicates.
	byGroup := map[string][]Candidate{}
	for _, c := range listings {
		g := NormalizeCompany(c.Company) + "|" + fold(c.Season)
		byGroup[g] = append(byGroup[g], c)
	}

	var pairs []DuplicatePair
	for _, group := range byGroup {
		words := make([][]string, len(group))
		for i, c := range group {
			words[i] = strings.Fields(NormalizeTitle(Posting{Title: c.Title, Season: c.Season}))
		}
		for i := range group {
			for j := i + 1; j < len(group); j++ {
				sim := jaccard(words[i], words[j])
				if sim >= minSimilarity && sim < 1 {
					pairs = append(pairs, DuplicatePair{A: group[i], B: group[j], Similarity: sim})
				}
			}
		}
	}

	// Most similar first; ties by id so the output is the same every run.
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Similarity != pairs[j].Similarity {
			return pairs[i].Similarity > pairs[j].Similarity
		}
		return pairs[i].A.ID < pairs[j].A.ID
	})
	return pairs
}

// jaccard is |words in both| / |words in either|.
func jaccard(a, b []string) float64 {
	inA := map[string]bool{}
	for _, w := range a {
		inA[w] = true
	}
	union := len(inA)
	both := 0
	seenB := map[string]bool{}
	for _, w := range b {
		if seenB[w] {
			continue
		}
		seenB[w] = true
		if inA[w] {
			both++
		} else {
			union++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(both) / float64(union)
}
