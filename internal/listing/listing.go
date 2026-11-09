// Package listing defines the common shape every source adapter produces,
// and the rules for deciding when two postings are the same job.
//
// Nothing here touches the network or the database, so it is easy to test.
package listing

import "time"

// JobType is what kind of role a listing is.
type JobType string

const (
	Internship JobType = "internship"
	NewGrad    JobType = "new_grad"
)

// DegreeLevel says whether a bachelor's student can apply.
type DegreeLevel string

const (
	BachelorsOK      DegreeLevel = "bachelors_ok"
	AdvancedRequired DegreeLevel = "advanced_required"
	DegreeUnknown    DegreeLevel = "unknown" // the source doesn't say
)

// Posting is one job ad as a single source shows it, for a single season.
// Adapters turn their raw format (JSON, Markdown, ...) into []Posting.
type Posting struct {
	SourceListingID string // the source's own id; stable across fetches
	Company         string
	Title           string
	JobType         JobType
	Season          string // e.g. "Summer 2027"; "" if the source doesn't say
	Locations       []string
	ApplyURL        string
	DegreeLevel     DegreeLevel
	Active          bool      // false when the source has marked it closed
	PostedAt        time.Time // when the source says it was posted; zero if unknown
	Section         string    // the source's own grouping, e.g. "Quant"; "" if none
}

// Listing is one real job: all postings that share a dedupe key.
type Listing struct {
	Key         string
	Company     string // display name, from the first posting
	Title       string // display title, from the first posting
	JobType     JobType
	Season      string
	DegreeLevel DegreeLevel
	PostedAt    time.Time // the earliest PostedAt of its postings; zero if none says
	Postings    []Posting
}

// AnyActive reports whether at least one posting is still open.
func (l Listing) AnyActive() bool {
	for _, p := range l.Postings {
		if p.Active {
			return true
		}
	}
	return false
}

// Group merges postings that describe the same job into Listings,
// keeping the order in which each job was first seen.
func Group(postings []Posting) []Listing {
	var listings []Listing
	indexByKey := map[string]int{}

	for _, p := range postings {
		// Store cities in one form, so lists from different sources merge cleanly.
		p.Locations = NormalizeLocations(p.Locations)
		key := DedupeKey(p)

		i, seen := indexByKey[key]
		if !seen {
			indexByKey[key] = len(listings)
			listings = append(listings, Listing{
				Key:         key,
				Company:     p.Company,
				Title:       p.Title,
				JobType:     p.JobType,
				Season:      p.Season,
				DegreeLevel: MergeDegree(p.DegreeLevel, DegreeUnknown), // "" (not set) becomes unknown
				PostedAt:    p.PostedAt,
				Postings:    []Posting{p},
			})
			continue
		}

		l := &listings[i]
		l.Postings = append(l.Postings, p)
		l.DegreeLevel = MergeDegree(l.DegreeLevel, p.DegreeLevel)
		l.PostedAt = Earliest(l.PostedAt, p.PostedAt)
	}
	return listings
}

// Earliest returns the earlier of two posted dates, treating zero as unknown.
func Earliest(a, b time.Time) time.Time {
	if a.IsZero() || (!b.IsZero() && b.Before(a)) {
		return b
	}
	return a
}

// MergeDegree combines what two postings of one job say about degrees.
// If any posting accepts bachelor's students, the job does. A posting that
// doesn't say (unknown) never overrides one that does.
func MergeDegree(a, b DegreeLevel) DegreeLevel {
	switch {
	case a == BachelorsOK || b == BachelorsOK:
		return BachelorsOK
	case a == AdvancedRequired || b == AdvancedRequired:
		return AdvancedRequired
	default:
		return DegreeUnknown
	}
}
