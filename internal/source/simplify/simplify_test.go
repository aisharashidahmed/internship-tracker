package simplify

import (
	"os"
	"slices"
	"testing"

	"internship-tracker/internal/listing"
)

func TestParse(t *testing.T) {
	body, err := os.ReadFile("testdata/listings.json")
	if err != nil {
		t.Fatal(err)
	}

	postings, err := Adapter{}.Parse(body)
	if err != nil {
		t.Fatal(err)
	}

	// Stripe has 2 terms -> 2 postings; Jane Street -> 1; the hidden record is skipped.
	if len(postings) != 3 {
		t.Fatalf("got %d postings, want 3: %+v", len(postings), postings)
	}

	stripeSummer, stripeFall, janeStreet := postings[0], postings[1], postings[2]
	if stripeSummer.Season != "Summer 2027" || stripeFall.Season != "Fall 2027" {
		t.Errorf("seasons = %q, %q; want Summer 2027, Fall 2027", stripeSummer.Season, stripeFall.Season)
	}
	if stripeSummer.SourceListingID != "a1" || stripeSummer.DegreeLevel != listing.BachelorsOK {
		t.Errorf("unexpected Stripe posting: %+v", stripeSummer)
	}
	if janeStreet.Active || janeStreet.DegreeLevel != listing.AdvancedRequired {
		t.Errorf("Jane Street should be inactive and PhD-only: %+v", janeStreet)
	}
}

func TestParseRejectsBadJSON(t *testing.T) {
	if _, err := (Adapter{}).Parse([]byte("<html>rate limited</html>")); err == nil {
		t.Error("expected an error for a non-JSON body")
	}
}

func TestSeasons_NAIsUnknownNotDropped(t *testing.T) {
	tests := []struct {
		in, want []string
	}{
		{[]string{"N/A"}, []string{""}},                           // unknown: kept as ""
		{[]string{"N/A", "Summer 2027"}, []string{"Summer 2027"}}, // a real season is enough
		{[]string{"Summer 2027", "Fall 2027"}, []string{"Summer 2027", "Fall 2027"}},
	}
	for _, tt := range tests {
		if got := seasons(tt.in); !slices.Equal(got, tt.want) {
			t.Errorf("seasons(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
