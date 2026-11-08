package listing

import "testing"

func TestPossibleDuplicates(t *testing.T) {
	listings := []Candidate{
		{ID: 1, Company: "Stripe", Title: "Software Engineer Intern, Payments", Season: "Summer 2027"},
		{ID: 2, Company: "Stripe, Inc.", Title: "Software Engineer Intern - Payments Platform", Season: "Summer 2027"},
		{ID: 3, Company: "Stripe", Title: "Software Engineer Intern, Payments", Season: "Fall 2027"},   // other season
		{ID: 4, Company: "Square", Title: "Software Engineer Intern, Payments", Season: "Summer 2027"}, // other company
		{ID: 5, Company: "Stripe", Title: "Product Designer Intern", Season: "Summer 2027"},            // not similar
		{ID: 6, Company: "Square", Title: "Software Engineer Intern - Payments", Season: ""},           // season unknown: compatible with #4
	}

	got := PossibleDuplicates(listings, 0.7)

	if len(got) != 2 {
		t.Fatalf("got %d pairs, want 2: %+v", len(got), got)
	}
	ids := map[[2]int64]bool{}
	for _, p := range got {
		ids[[2]int64{p.A.ID, p.B.ID}] = true
	}
	if !ids[[2]int64{1, 2}] || !ids[[2]int64{4, 6}] {
		t.Errorf("got pairs %v, want 1/2 and 4/6", ids)
	}
}

func TestJaccard(t *testing.T) {
	if got := jaccard([]string{"a", "b", "c"}, []string{"a", "b", "d"}); got != 0.5 {
		t.Errorf("jaccard = %v, want 0.5", got)
	}
}
