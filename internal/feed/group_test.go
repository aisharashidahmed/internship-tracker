package feed

import (
	"testing"
	"time"

	"internship-tracker/internal/store"
)

func TestGroup(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 11, 8, 21, 0, 0, 0, ny)    // 9pm Nov 8 in New York (Nov 9 in UTC)
	earlier := time.Date(2026, 11, 8, 8, 0, 0, 0, ny) // this morning
	lastWeek := now.AddDate(0, 0, -7)

	listings := []store.FeedListing{
		{ID: 1, GroupKey: "tesla|swe", Company: "Tesla", Title: "SWE Intern - Summer 2027", Season: "Summer 2027", PostedAt: &earlier, Roles: []string{"general_swe"}},
		{ID: 2, GroupKey: "tesla|swe", Company: "Tesla", Title: "SWE Intern", Season: "Spring 2027", PostedAt: &lastWeek, Roles: []string{"backend"}},
		{ID: 3, GroupKey: "figma|design", Company: "Figma, Inc.", Title: "Design Intern", PostedAt: &lastWeek},
		// First seen today, but the source says it was posted last week: not "Posted today".
		{ID: 4, GroupKey: "stryten|eng", Company: "Stryten", Title: "Engineering Intern", PostedAt: &lastWeek, FirstSeenAt: earlier, FeedAt: &earlier},
		// No posted date at all: first seen today counts.
		{ID: 5, GroupKey: "acme|x", Company: "Acme", Title: "X Intern", FirstSeenAt: earlier},
	}
	cards := Group(listings, nil, now, ny)

	if len(cards) != 4 || len(cards[0].Seasons) != 2 || cards[0].Seasons[0].Season != "Summer 2027" {
		t.Fatalf("want 4 cards, Tesla with Summer then Spring; got %+v", cards)
	}
	if cards[2].Seasons[0].PostedToday {
		t.Error("posted last week but first seen today must not say Posted today")
	}
	if !cards[3].Seasons[0].PostedToday {
		t.Error("no posted date, first seen today: Posted today")
	}
	if !cards[0].Seasons[0].PostedToday || cards[0].Seasons[1].PostedToday {
		t.Error("Posted today: this morning in New York is today; last week is not")
	}
	if got := cards[0].Roles; len(got) != 2 {
		t.Errorf("card roles = %v, want both seasons' roles", got)
	}
	if cards[1].Initial != "F" || cards[1].Color != CompanyColor("Figma") {
		t.Errorf("Figma circle = %q color %d; the color should match plain Figma (same normalized name)", cards[1].Initial, cards[1].Color)
	}
}

func TestCompanyColorIsStableAndInRange(t *testing.T) {
	for _, name := range []string{"Stripe", "Tesla", "1Password", "ByteDance"} {
		c := CompanyColor(name)
		if c != CompanyColor(name) || c < 0 || c >= CompanyColors {
			t.Errorf("CompanyColor(%q) = %d", name, c)
		}
	}
	if initial("1Password") != "1" || initial("  (SIG)") != "S" {
		t.Error("initial should skip punctuation and keep digits")
	}
}
