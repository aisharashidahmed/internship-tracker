package speedyapply

import (
	"os"
	"strings"
	"testing"
	"time"

	"internship-tracker/internal/listing"
)

// testdata/README.md is a trimmed copy of the real file: one table with a
// Salary column, one ("Other") without.
func TestParse(t *testing.T) {
	body, err := os.ReadFile("testdata/README.md")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Adapter{}.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d postings, want 3", len(got))
	}

	tiktok, citadel, astranis := got[0], got[1], got[2]

	if !tiktok.TopCompany || citadel.TopCompany || astranis.TopCompany {
		t.Error("only the FAANG+ section's companies are top companies")
	}
	if tiktok.Section != "FAANG+" || citadel.Section != "Quant" || astranis.Section != "Other" {
		t.Errorf("sections = %q, %q, %q; want FAANG+, Quant, Other", tiktok.Section, citadel.Section, astranis.Section)
	}

	if tiktok.Company != "TikTok" || tiktok.Season != "" {
		t.Errorf("TikTok: company %q season %q; want TikTok and unknown season (title only says 2027)",
			tiktok.Company, tiktok.Season)
	}
	if tiktok.DegreeLevel != listing.DegreeUnknown || !tiktok.Active {
		t.Errorf("TikTok: degree %s active %v; want unknown and active", tiktok.DegreeLevel, tiktok.Active)
	}

	// "Houston, TX +2": keep the named city, drop the count.
	if strings.Join(citadel.Locations, ";") != "Houston, TX" {
		t.Errorf("Citadel locations = %q, want [Houston, TX]", citadel.Locations)
	}
	// A generic link has no job identity, so the raw URL is the id.
	if citadel.SourceListingID != citadel.ApplyURL {
		t.Errorf("Citadel id = %q, want the raw URL", citadel.SourceListingID)
	}

	// "Other" table, no Salary column: columns must still line up.
	if astranis.Season != "Winter 2027" || astranis.Locations[0] != "San Francisco, CA" {
		t.Errorf("Astranis: season %q locations %q", astranis.Season, astranis.Locations)
	}
	if astranis.SourceListingID != "greenhouse:4704571006" {
		t.Errorf("Astranis id = %q, want greenhouse:4704571006", astranis.SourceListingID)
	}
}

func TestParseFailsLoudlyOnFormatChange(t *testing.T) {
	renamed := "| Company | Role | Location | Posting | Age |\n|---|---|---|---|---|\n| a | b | c | d | e |\n"
	if _, err := (Adapter{}).Parse([]byte(renamed)); err == nil {
		t.Error("want an error when the Position column is renamed")
	}

	extraCell := "| Company | Position | Location | Posting | Age |\n|---|---|---|---|---|\n| a | b | c | x | y | z |\n"
	if _, err := (Adapter{}).Parse([]byte(extraCell)); err == nil {
		t.Error("want an error when a row has more cells than the header")
	}
}

func TestParsePostedDateFromAge(t *testing.T) {
	body, _ := os.ReadFile("testdata/README.md")
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	got, err := Adapter{Now: func() time.Time { return now }}.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	// TikTok's row says "2d": downloaded Nov 8, so posted Nov 6.
	if want := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC); !got[0].PostedAt.Equal(want) {
		t.Errorf("TikTok posted %v, want %v", got[0].PostedAt, want)
	}
}
