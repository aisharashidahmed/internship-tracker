// Package feed turns stored listings into the cards the feed shows.
// It holds the display rules (city summary, openings count) and no I/O,
// so the web pages and the JSON API show exactly the same thing.
package feed

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"internship-tracker/internal/listing"
	"internship-tracker/internal/store"
)

// Card is one listing in the feed. It is also the JSON shape of /api/listings.
type Card struct {
	ID       int64  `json:"id"`
	Company  string `json:"company"`
	Title    string `json:"title"`
	Season   string `json:"season,omitempty"` // omitted if unknown
	Where    string `json:"where"`            // "Sunnyvale + 4 more"
	Openings int    `json:"openings"`         // distinct job links still listed as active
	Links    []Link `json:"links"`
	// Tags, as stable ids ("ml", "healthcare"); labels are for the page only.
	Roles      []string `json:"roles"`
	Industries []string `json:"industries"`
	// Stale: no source says closed, but none still lists it: "may be closed".
	Stale              bool       `json:"stale"`
	AdvancedDegreeOnly bool       `json:"advanced_degree_only"`
	PostedAt           *time.Time `json:"posted_at,omitempty"`
	// New: appeared after you last looked. Always false when logged out.
	New         bool      `json:"new"`
	FirstSeenAt time.Time `json:"first_seen_at"`
}

// Link is one opening: a job link, with every source that lists it.
type Link struct {
	URL          string     `json:"url"`
	Sources      []string   `json:"sources"`
	Cities       []string   `json:"cities"`
	NotSeenSince *time.Time `json:"not_seen_since,omitempty"` // set if every source stopped listing it
}

// NewCard builds the card for one listing. newSince is the viewer's
// "new since you last looked" boundary, or nil when logged out.
//
// Only active postings count: a link a source marked closed is not an
// opening. A link two sources both list (same job link) is one opening.
func NewCard(l store.FeedListing, newSince *time.Time) Card {
	c := Card{
		ID:                 l.ID,
		Company:            l.Company,
		Title:              l.Title,
		Season:             l.Season,
		Stale:              l.Status == store.StatusStale,
		AdvancedDegreeOnly: l.DegreeLevel == "advanced_required",
		PostedAt:           l.PostedAt,
		FirstSeenAt:        l.FirstSeenAt,
		Links:              []Link{},
		New:                newSince != nil && l.FeedAt != nil && l.FeedAt.After(*newSince),
		Roles:              sortedRoles(l.Roles),
		Industries:         append([]string{}, l.Industries...),
	}

	linkIndex := map[string]int{} // job identity -> index in c.Links
	var cities []string
	for _, p := range l.Postings {
		if !p.Active {
			continue
		}
		cities = appendNew(cities, p.Locations...)

		identity := p.ApplyURL // a generic link is only "the same" if spelled the same
		if p.JobLink != nil {
			identity = *p.JobLink
		}
		i, seen := linkIndex[identity]
		if !seen {
			i = len(c.Links)
			linkIndex[identity] = i
			c.Links = append(c.Links, Link{URL: p.ApplyURL, NotSeenSince: p.MissingSince})
		}
		link := &c.Links[i]
		link.Sources = appendNew(link.Sources, p.Source)
		link.Cities = appendNew(link.Cities, p.Locations...)
		if p.MissingSince == nil {
			link.NotSeenSince = nil // some source still lists it
		}
	}

	c.Openings = len(c.Links)
	c.Where = where(cities)
	return c
}

// sortedRoles puts roles in the filter bar's order, so chips line up.
func sortedRoles(roles []string) []string {
	out := append([]string{}, roles...)
	slices.SortFunc(out, func(a, b string) int {
		return slices.Index(listing.AllRoles, listing.Role(a)) - slices.Index(listing.AllRoles, listing.Role(b))
	})
	return out
}

// where summarizes a city list: "Sunnyvale", "Sunnyvale + 4 more".
func where(locations []string) string {
	if len(locations) == 0 {
		return "Location unknown"
	}
	first := strings.Split(locations[0], ",")[0] // "Sunnyvale, CA" -> "Sunnyvale"
	if len(locations) == 1 {
		return first
	}
	return fmt.Sprintf("%s + %d more", first, len(locations)-1)
}

// appendNew appends the values not already in list, keeping order.
func appendNew(list []string, values ...string) []string {
	for _, v := range values {
		found := false
		for _, have := range list {
			if have == v {
				found = true
				break
			}
		}
		if !found {
			list = append(list, v)
		}
	}
	return list
}
