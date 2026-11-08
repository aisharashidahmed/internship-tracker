package listing

import "testing"

func TestPossibleDuplicates(t *testing.T) {
	listings := []Candidate{
		{ID: 1, Company: "Stripe", Title: "Software Engineer Intern, Payments", Season: "Summer 2027"},
		{ID: 2, Company: "Stripe, Inc.", Title: "Software Engineer Intern - Payments Platform", Season: "Summer 2027"},
		{ID: 3, Company: "Stripe", Title: "Software Engineer Intern, Payments", Season: "Fall 2027"},   // other season
		{ID: 4, Company: "Square", Title: "Software Engineer Intern, Payments", Season: "Summer 2027"}, // other company
		{ID: 5, Company: "Stripe", Title: "Product Designer Intern", Season: "Summer 2027"},            // not similar
	}

	got := PossibleDuplicates(listings, 0.7)

	if len(got) != 1 {
		t.Fatalf("got %d pairs, want 1: %+v", len(got), got)
	}
	if got[0].A.ID != 1 || got[0].B.ID != 2 {
		t.Errorf("got pair %d/%d, want 1/2", got[0].A.ID, got[0].B.ID)
	}
}

func TestJaccard(t *testing.T) {
	if got := jaccard([]string{"a", "b", "c"}, []string{"a", "b", "d"}); got != 0.5 {
		t.Errorf("jaccard = %v, want 0.5", got)
	}
}
