package feed

import (
	"strings"
	"testing"
	"time"

	"internship-tracker/internal/store"
)

func ptr[T any](v T) *T { return &v }

func TestNewCard_OpeningsAndWhere(t *testing.T) {
	l := store.FeedListing{
		ID: 1, Company: "Nokia", Title: "AI R&D Engineer Co-op", Status: store.StatusOpen,
		Postings: []store.FeedPosting{
			{Source: "simplify", ApplyURL: "https://nokia.wd3.myworkdayjobs.com/x/job/Sunnyvale/A_R1",
				JobLink: ptr("workday:nokia:R1"), Locations: []string{"Sunnyvale, CA"}, Active: true},
			// Same job via SpeedyApply, spelled differently: still one opening.
			{Source: "speedyapply", ApplyURL: "https://nokia.wd3.myworkdayjobs.com/en-US/x/job/A_R1",
				JobLink: ptr("workday:nokia:R1"), Locations: []string{"Sunnyvale, CA"}, Active: true},
			{Source: "simplify", ApplyURL: "https://nokia.wd3.myworkdayjobs.com/x/job/Dallas/A_R2",
				JobLink: ptr("workday:nokia:R2"), Locations: []string{"Dallas, TX", "Remote"}, Active: true},
			// Closed by its source: not an opening, and its city isn't shown.
			{Source: "simplify", ApplyURL: "https://nokia.wd3.myworkdayjobs.com/x/job/Austin/A_R3",
				JobLink: ptr("workday:nokia:R3"), Locations: []string{"Austin, TX"}, Active: false},
		},
	}

	c := NewCard(l, nil)

	if c.Openings != 2 {
		t.Errorf("openings = %d, want 2 (R1 in both sources counts once; R3 is closed)", c.Openings)
	}
	if c.Where != "Sunnyvale + 2 more" {
		t.Errorf("where = %q, want %q", c.Where, "Sunnyvale + 2 more")
	}
	if got := NewCard(store.FeedListing{Roles: []string{"ml", "backend"}}, nil).Roles; strings.Join(got, ",") != "backend,ml" {
		t.Errorf("roles = %q, want filter-bar order backend,ml", got)
	}
	if got := c.Links[0].Sources; len(got) != 2 {
		t.Errorf("R1 sources = %q, want both", got)
	}
}

func TestNewCard_GenericLinksCountedBySpelling(t *testing.T) {
	l := store.FeedListing{Postings: []store.FeedPosting{
		{Source: "simplify", ApplyURL: "https://figma.com/careers", Active: true},
		{Source: "speedyapply", ApplyURL: "https://figma.com/careers", Active: true},
	}}
	if c := NewCard(l, nil); c.Openings != 1 || c.Where != "Location unknown" {
		t.Errorf("openings = %d where = %q, want 1 and Location unknown", c.Openings, c.Where)
	}
}

func TestNewCard_NotSeenSinceOnlyIfNoSourceListsIt(t *testing.T) {
	gone := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	l := store.FeedListing{Status: store.StatusOpen, Postings: []store.FeedPosting{
		{Source: "speedyapply", ApplyURL: "https://x.com/jobs/12345", JobLink: ptr("x.com/jobs/12345"), Active: true, MissingSince: &gone},
		{Source: "simplify", ApplyURL: "https://x.com/jobs/12345", JobLink: ptr("x.com/jobs/12345"), Active: true},
	}}
	if c := NewCard(l, nil); c.Links[0].NotSeenSince != nil {
		t.Error("Simplify still lists the link, so it should not show 'not seen since'")
	}
}

func TestNewCard_New(t *testing.T) {
	since := time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
	before, after := since.Add(-time.Minute), since.Add(time.Minute)
	if !NewCard(store.FeedListing{NewAt: &after}, &since).New {
		t.Error("appeared after the boundary but not New")
	}
	if NewCard(store.FeedListing{NewAt: &before}, &since).New {
		t.Error("appeared before the boundary but New")
	}
	if NewCard(store.FeedListing{NewAt: &after}, nil).New {
		t.Error("logged out: nothing is New")
	}
}

func TestNewCard_YourApplication(t *testing.T) {
	id, stage, round := int64(7), "interview", 2
	c := NewCard(store.FeedListing{ApplicationID: &id, Stage: &stage, InterviewRound: &round}, nil)
	if c.ApplicationID != 7 || c.StageLabel != "Interview · round 2" {
		t.Errorf("card = %d %q", c.ApplicationID, c.StageLabel)
	}
}
