package simplify

import (
	"errors"
	"html"
	"regexp"
	"strings"
)

// Simplify's JSON has no "FAANG+" field; only its README marks those
// companies, with a 🔥 in the company cell of each row:
//
//	<tr>
//	<td>🔥 <strong><a href="https://simplify.jobs/c/TikTok?...">TikTok</a></strong></td>
var (
	firstCell = regexp.MustCompile(`(?s)<tr>\s*<td>(.*?)</td>`)
	htmlTag   = regexp.MustCompile(`<[^>]*>`)
)

const fire = "🔥"

// TopCompanies returns the company names Simplify's README marks with 🔥.
// The marker is read from the raw cell first; only then is the name
// cleaned up (tags, entities, the emoji itself), so cleanup can never
// lose the flag. It fails if the README has no table rows at all: that
// means the format changed, and an empty answer would wrongly un-flag
// every company.
func TopCompanies(readme []byte) ([]string, error) {
	cells := firstCell.FindAllStringSubmatch(string(readme), -1)
	if len(cells) == 0 {
		return nil, errors.New("simplify README: no table rows found (format changed?)")
	}
	seen := map[string]bool{}
	var names []string
	for _, c := range cells {
		raw := c[1]
		if !strings.Contains(raw, fire) { // decide on the raw text, before any cleanup
			continue
		}
		name := strings.TrimSpace(strings.ReplaceAll(html.UnescapeString(htmlTag.ReplaceAllString(raw, "")), fire, ""))
		if name == "" || name == "↳" || seen[name] { // "↳" rows continue the company above
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names, nil
}
