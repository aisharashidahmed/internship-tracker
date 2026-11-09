package feed

import (
	"hash/fnv"
	"strings"
	"time"
	"unicode"

	"internship-tracker/internal/listing"
	"internship-tracker/internal/store"
)

// RoleCard is one card in the feed: one role at one company, with a
// chip per season. Each season is its own listing (with its own links and
// your own application); the card only shows them together.
type RoleCard struct {
	Key     string `json:"key"`
	Company string `json:"company"`
	Title   string `json:"title"` // the newest season's wording
	// Initial and Color draw the company circle: Color picks one of
	// CompanyColors, from the company's name, so a company always gets
	// the same color on every page and device.
	Initial string `json:"initial"`
	Color   int    `json:"color"`
	// TopCompany: a source flags the company as a top company (FAANG+).
	TopCompany bool `json:"top_company"`
	// CompanyID and Starred drive the card\'s star button.
	CompanyID int64 `json:"company_id"`
	Starred   bool  `json:"starred"`
	// Roles and Industries are the union over the seasons, for the chips.
	Roles      []string `json:"roles"`
	Industries []string `json:"industries"`
	// Seasons, newest first; the first is selected by default.
	Seasons []Card `json:"seasons"`
}

// CompanyColors is how many colors the company circles cycle through
// (the palette itself is in the stylesheet, so it can differ per theme).
const CompanyColors = 8

// Group builds the cards for a page of listings, which arrive card by card
// (see store.Feed). now and loc decide "Posted today".
func Group(listings []store.FeedListing, newSince *time.Time, now time.Time, loc *time.Location) []RoleCard {
	var cards []RoleCard
	for _, l := range listings {
		c := NewCard(l, newSince)
		c.PostedToday = postedToday(l, now, loc)
		if n := len(cards); n > 0 && cards[n-1].Key == l.GroupKey {
			last := &cards[n-1]
			last.Seasons = append(last.Seasons, c)
			last.Roles = union(last.Roles, c.Roles)
			last.Industries = union(last.Industries, c.Industries)
			continue
		}
		cards = append(cards, RoleCard{
			Key: l.GroupKey, Company: l.Company, Title: l.Title,
			Initial: initial(l.Company), Color: CompanyColor(l.Company), TopCompany: l.TopCompany,
			CompanyID: l.CompanyID, Starred: l.Starred,
			Roles: c.Roles, Industries: c.Industries, Seasons: []Card{c},
		})
	}
	for i := range cards {
		cards[i].Roles = sortedRoles(cards[i].Roles)
	}
	return cards
}

// postedToday: the source says it was posted today (in the viewer's
// timezone). Only when no source gives a date do we use the day we first
// saw it, so the chip never contradicts the card's "posted 69 days ago".
func postedToday(l store.FeedListing, now time.Time, loc *time.Location) bool {
	if l.PostedAt != nil {
		return sameDay(*l.PostedAt, now, loc)
	}
	return sameDay(l.FirstSeenAt, now, loc)
}

// CompanyColor picks a stable color index for a company: a hash of its
// normalized name, so "Stripe" and "Stripe, Inc." match.
func CompanyColor(company string) int {
	h := fnv.New32a()
	h.Write([]byte(listing.NormalizeCompany(company)))
	return int(h.Sum32() % CompanyColors)
}

// initial is the first letter or digit of a company name, uppercased.
func initial(company string) string {
	for _, r := range company {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return strings.ToUpper(string(r))
		}
	}
	return "?"
}

// sameDay reports whether a and b fall on the same calendar day in loc.
func sameDay(a, b time.Time, loc *time.Location) bool {
	ya, ma, da := a.In(loc).Date()
	yb, mb, db := b.In(loc).Date()
	return ya == yb && ma == mb && da == db
}

func union(a, b []string) []string {
	return appendNew(append([]string{}, a...), b...)
}
