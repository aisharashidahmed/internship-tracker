// Package simplify reads SimplifyJobs/Summer2027-Internships, which stores
// every listing in one JSON file (.github/scripts/listings.json).
package simplify

import (
	"encoding/json"
	"fmt"
	"time"

	"internship-tracker/internal/listing"
)

// record is one entry of listings.json, keeping only the fields we use.
//
//	{"id": "fbc8...", "company_name": "Nokia", "title": "Software Developer Intern",
//	 "active": true, "is_visible": true, "terms": ["Summer 2027"],
//	 "url": "https://...", "locations": ["Naperville, IL"], "degrees": ["Bachelor's"]}
type record struct {
	ID        string   `json:"id"`
	Company   string   `json:"company_name"`
	Title     string   `json:"title"`
	Active    bool     `json:"active"`
	IsVisible bool     `json:"is_visible"`
	Terms     []string `json:"terms"`
	URL       string   `json:"url"`
	Locations []string `json:"locations"`
	Degrees   []string `json:"degrees"`
	Posted    int64    `json:"date_posted"` // Unix seconds
}

// Adapter implements source.Adapter for Simplify.
type Adapter struct{}

func (Adapter) Name() string { return "simplify" }

// Parse turns listings.json into postings. A record listing several terms
// ("Summer 2027", "Fall 2027") becomes one posting per term, because each
// season is a separate listing.
func (Adapter) Parse(body []byte) ([]listing.Posting, error) {
	var records []record
	if err := json.Unmarshal(body, &records); err != nil {
		return nil, fmt.Errorf("simplify: decode listings.json: %w", err)
	}

	var postings []listing.Posting
	for _, r := range records {
		if !r.IsVisible { // hidden by Simplify's moderators
			continue
		}
		for _, term := range r.Terms {
			postings = append(postings, listing.Posting{
				SourceListingID: r.ID,
				Company:         r.Company,
				Title:           r.Title,
				JobType:         listing.JobTypeFromTitle(r.Title, listing.Internship), // an internship list, with a few new-grad roles
				Season:          term,
				Locations:       r.Locations,
				ApplyURL:        r.URL,
				DegreeLevel:     degreeLevel(r.Degrees),
				Active:          r.Active,
				PostedAt:        postedAt(r.Posted),
			})
		}
	}
	return postings, nil
}

// postedAt converts Unix seconds; 0 means Simplify didn't say.
func postedAt(unix int64) time.Time {
	if unix <= 0 {
		return time.Time{}
	}
	return time.Unix(unix, 0).UTC()
}

// degreeLevel: a role is open to bachelor's students if it lists
// "Bachelor's" or lists no degree requirement at all.
func degreeLevel(degrees []string) listing.DegreeLevel {
	if len(degrees) == 0 {
		return listing.BachelorsOK
	}
	for _, d := range degrees {
		if d == "Bachelor's" {
			return listing.BachelorsOK
		}
	}
	return listing.AdvancedRequired
}
