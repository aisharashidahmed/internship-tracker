// Package speedyapply reads speedyapply/2027-SWE-College-Jobs, whose
// README.md lists US internships in Markdown tables:
//
//	| Company | Position | Location | Salary | Posting | Age |   (FAANG+, Quant)
//	| Company | Position | Location | Posting | Age |            (Other: no Salary)
//
// The source has no ids, no "closed" flag, no season and no degree field,
// so those come from the title (season) or stay unknown (degree).
package speedyapply

import (
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"

	"internship-tracker/internal/listing"
)

// Adapter implements source.Adapter for SpeedyApply.
type Adapter struct {
	// Now is the time the file was downloaded; the "Age" column ("23d")
	// counts back from it. Nil means time.Now. Tests set a fixed time.
	Now func() time.Time
}

func (Adapter) Name() string { return "speedyapply" }

// requiredColumns must appear in every table's header. If SpeedyApply
// renames one, we fail loudly instead of reading the wrong column.
var requiredColumns = []string{"Company", "Position", "Location", "Posting"}

// Parse turns README.md into postings. Every row is an open job: SpeedyApply
// removes rows instead of marking them closed.
func (a Adapter) Parse(body []byte) ([]listing.Posting, error) {
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}

	var postings []listing.Posting
	var columns map[string]int // column name -> index, from the current table's header

	for n, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			columns = nil // a table ends at the first non-table line
			continue
		}
		cells := splitRow(line)

		switch {
		case columns == nil: // the first row of a table is its header
			var err error
			if columns, err = headerColumns(cells); err != nil {
				return nil, fmt.Errorf("speedyapply: line %d: %w", n+1, err)
			}
		case isDivider(cells): // |---|---|
		default:
			p, err := parseRow(cells, columns, now)
			if err != nil {
				return nil, fmt.Errorf("speedyapply: line %d: %w", n+1, err)
			}
			postings = append(postings, p)
		}
	}
	return postings, nil
}

func parseRow(cells []string, columns map[string]int, now time.Time) (listing.Posting, error) {
	if len(cells) != len(columns) {
		return listing.Posting{}, fmt.Errorf("row has %d cells, header has %d", len(cells), len(columns))
	}
	applyURL := lastHref(cells[columns["Posting"]])
	if applyURL == "" {
		return listing.Posting{}, fmt.Errorf("no apply link in %q", cells[columns["Posting"]])
	}
	title := text(cells[columns["Position"]])

	// No ids: the job's link identity is the most stable thing we have.
	id, ok := listing.JobLink(applyURL)
	if !ok {
		id = applyURL
	}

	return listing.Posting{
		SourceListingID: id,
		Company:         text(cells[columns["Company"]]),
		Title:           title,
		JobType:         listing.Internship, // README.md only lists internships
		Season:          listing.SeasonFromTitle(title),
		Locations:       locations(text(cells[columns["Location"]])),
		ApplyURL:        applyURL,
		DegreeLevel:     listing.DegreeUnknown,
		Active:          true,
		PostedAt:        postedAt(cells, columns, now),
	}, nil
}

var age = regexp.MustCompile(`^(\d+)d$`) // "23d"

// postedAt turns the Age column into a date: "23d" downloaded at noon on
// Nov 8 is Oct 16. If the column is missing or unreadable the date is
// unknown (zero); that only affects sorting, so it isn't worth failing for.
func postedAt(cells []string, columns map[string]int, now time.Time) time.Time {
	i, ok := columns["Age"]
	if !ok {
		return time.Time{}
	}
	m := age.FindStringSubmatch(text(cells[i]))
	if m == nil {
		return time.Time{}
	}
	days, _ := strconv.Atoi(m[1])
	return now.AddDate(0, 0, -days)
}

func headerColumns(cells []string) (map[string]int, error) {
	columns := map[string]int{}
	for i, c := range cells {
		columns[c] = i
	}
	for _, name := range requiredColumns {
		if _, ok := columns[name]; !ok {
			return nil, fmt.Errorf("table header %q has no %q column (format changed?)", cells, name)
		}
	}
	return columns, nil
}

// splitRow turns "| a | b |" into ["a", "b"].
func splitRow(line string) []string {
	parts := strings.Split(strings.Trim(line, "|"), "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func isDivider(cells []string) bool {
	for _, c := range cells {
		if strings.Trim(c, "-: ") != "" {
			return false
		}
	}
	return true
}

var (
	tag          = regexp.MustCompile(`<[^>]*>`)
	href         = regexp.MustCompile(`href="([^"]+)"`)
	moreLocation = regexp.MustCompile(`\s*\+\d+$`) // "Houston, TX +2"
)

// text strips HTML tags and entities: `<a ...><strong>Notion</strong></a>` -> "Notion".
func text(cell string) string {
	return strings.TrimSpace(html.UnescapeString(tag.ReplaceAllString(cell, "")))
}

// lastHref returns the last link in a cell (the "Apply" button).
func lastHref(cell string) string {
	m := href.FindAllStringSubmatch(cell, -1)
	if m == nil {
		return ""
	}
	return html.UnescapeString(m[len(m)-1][1])
}

// locations keeps the one city SpeedyApply shows. "+2" means two more
// cities it doesn't name, so we can't store them.
func locations(cell string) []string {
	loc := moreLocation.ReplaceAllString(cell, "")
	if loc == "" {
		return nil
	}
	return []string{loc}
}
