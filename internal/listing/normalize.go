package listing

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// nonAlphanumeric matches every run of characters that isn't a-z or 0-9.
// Emoji, dashes of every kind ("-", "–", "—") and quotes all fall in here,
// so they all turn into a single space.
var nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

// fold is the first step of every normalization: lowercase, drop accents,
// turn all punctuation and emoji into spaces, and trim.
//
//	"AtkinsRéalis"                    -> "atkinsrealis"
//	"Software Engineer – Intern 🚀"   -> "software engineer intern"
func fold(s string) string {
	// NFD splits "é" into "e" + a combining accent mark; we then drop the mark.
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	s = strings.ReplaceAll(b.String(), "&", " and ")
	s = nonAlphanumeric.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// --- Companies ---------------------------------------------------------------

// legalSuffixes are dropped from the end of company names, so
// "Stripe", "Stripe, Inc." and "STRIPE INC" all become "stripe".
var legalSuffixes = map[string]bool{
	"inc": true, "incorporated": true, "llc": true, "ltd": true, "limited": true,
	"corp": true, "corporation": true, "plc": true, "gmbh": true, "co": true,
}

// companyAliases maps a company's other names to the one we use.
// Hand-maintained and deliberately small: a wrong entry would merge two
// different companies' jobs, which is worse than a missed duplicate.
// Keys and values are already normalized.
var companyAliases = map[string]string{
	"facebook":       "meta",
	"meta platforms": "meta",
}

// parenthetical matches "(...)", e.g. the nickname in "Susquehanna (SIG)".
var parenthetical = regexp.MustCompile(`\([^)]*\)`)

// NormalizeCompany returns the name used to recognize the same company
// across sources. It is what companies.normalized_name stores.
func NormalizeCompany(name string) string {
	// A nickname in parentheses is extra; sources disagree on including it.
	words := strings.Fields(fold(parenthetical.ReplaceAllString(name, " ")))
	// Strip suffixes from the end, but never strip the whole name
	// (a company literally called "Co" stays "co").
	for len(words) > 1 && legalSuffixes[words[len(words)-1]] {
		words = words[:len(words)-1]
	}
	normalized := strings.Join(words, " ")
	if alias, ok := companyAliases[normalized]; ok {
		return alias
	}
	return normalized
}

// --- Titles ------------------------------------------------------------------

// seasonWords matches a season *with* a year ("summer 2027", "2027 summer",
// "summer 27"), or a year on its own. A bare "summer" is never matched,
// because in "Summer Analyst" it is part of the job's name.
// Group 1 or 2 holds the season name, if there is one.
var seasonWords = regexp.MustCompile(
	`\b(?:(summer|fall|autumn|winter|spring)\s+(?:20)?\d{2}|20\d{2}(?:\s+(summer|fall|autumn|winter|spring))?)\b`)

// stripSeason removes season phrases that agree with the posting's own
// season. A phrase that disagrees ("Spring 2027" in a title Simplify tagged
// Summer) is kept, so that posting can't merge into the wrong season's listing.
func stripSeason(title, season string) string {
	own := seasonName(season)
	return seasonWords.ReplaceAllStringFunc(title, func(phrase string) string {
		m := seasonWords.FindStringSubmatch(phrase)
		name := seasonName(m[1] + m[2])
		if name == "" || name == own { // a bare year, or the same season
			return " "
		}
		return phrase
	})
}

// SeasonFromTitle reads a season written in a title, for sources without a
// season field: "SWE Intern - Summer 2027" -> "Summer 2027". It returns ""
// unless the title names both a season and a year; it never guesses.
func SeasonFromTitle(title string) string {
	for _, m := range seasonWords.FindAllStringSubmatch(fold(title), -1) {
		name := seasonName(m[1] + m[2])
		year := yearDigits.FindString(m[0])
		if name == "" {
			continue // a bare year says nothing about the season
		}
		if len(year) == 2 {
			year = "20" + year
		}
		return strings.ToUpper(name[:1]) + name[1:] + " " + year
	}
	return ""
}

// yearDigits finds the year inside a season phrase ("summer 27" -> "27").
var yearDigits = regexp.MustCompile(`\d+`)

// seasonName returns "summer" for "Summer 2027", treating autumn as fall.
func seasonName(season string) string {
	words := strings.Fields(fold(season))
	if len(words) == 0 {
		return ""
	}
	if words[0] == "autumn" {
		return "fall"
	}
	return words[0]
}

// titleSynonyms rewrite one word into the form we compare on.
// Applied word by word, after folding.
var titleSynonyms = map[string]string{
	"swe":         "software engineer",
	"sde":         "software development engineer",
	"ml":          "machine learning",
	"engineering": "engineer",
	"internship":  "intern",
}

// NormalizeTitle returns the posting's title as used for matching. It uses
// the posting's own season and cities: "SWE Intern - New York - Summer 2027"
// is the same job as "SWE Intern" when the posting is for Summer 2027 in New York.
//
// Team names are kept: "SWE Intern, Infrastructure" and "SWE Intern, Mobile"
// stay different.
func NormalizeTitle(p Posting) string {
	s := stripSeason(fold(p.Title), p.Season)
	words := stripTrailingLocation(strings.Fields(s), p.Locations)

	var out []string
	for _, w := range words {
		if syn, ok := titleSynonyms[w]; ok {
			w = syn
		}
		out = append(out, w)
	}
	return strings.Join(out, " ")
}

// stripTrailingLocation removes location words from the end of a title,
// but only words that name one of the posting's own locations (or "remote"
// / "hybrid"), so a team name is never mistaken for a city. It always
// leaves at least one word.
func stripTrailingLocation(words, locations []string) []string {
	own := map[string]bool{"remote": true, "hybrid": true}
	for _, l := range NormalizeLocations(locations) {
		own[fold(l)] = true                        // "new york ny"
		own[fold(strings.Split(l, ",")[0])] = true // "new york"
	}

	for stripped := true; stripped; {
		stripped = false
		// Try the longest tail first, so "new york ny" goes in one step.
		for n := min(4, len(words)-1); n >= 1; n-- {
			tail := strings.Join(words[len(words)-n:], " ")
			if own[tail] || own[fold(NormalizeLocation(tail))] {
				words = words[:len(words)-n]
				stripped = true
				break
			}
		}
	}
	return words
}

// --- Locations ---------------------------------------------------------------

// locationAliases maps folded spellings to the one form we store.
var locationAliases = map[string]string{
	"nyc":              "New York, NY",
	"new york city":    "New York, NY",
	"new york city ny": "New York, NY",
	"new york":         "New York, NY",
	"new york ny":      "New York, NY",
	"sf":               "San Francisco, CA",
	"san francisco":    "San Francisco, CA",
	"south sf":         "South San Francisco, CA",
	"la":               "Los Angeles, CA",
	"los angeles":      "Los Angeles, CA",
}

// NormalizeLocation returns one form per place. Every remote variant
// ("Remote", "Remote in USA", "Remote - US") becomes "Remote".
func NormalizeLocation(loc string) string {
	folded := fold(loc)
	if folded == "remote" || strings.HasPrefix(folded, "remote ") {
		return "Remote"
	}
	if alias, ok := locationAliases[folded]; ok {
		return alias
	}
	return strings.Join(strings.Fields(loc), " ") // tidy spacing, keep as given
}

// NormalizeLocations normalizes each location and drops repeats,
// keeping the original order.
func NormalizeLocations(locs []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range locs {
		n := NormalizeLocation(l)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// --- Dedupe key --------------------------------------------------------------

// DedupeKey identifies one real job. Two postings with the same key are the
// same listing: company + title + job type + season.
//
//	"stripe|software engineer intern|internship|summer 2027"
func DedupeKey(p Posting) string {
	return strings.Join([]string{
		NormalizeCompany(p.Company),
		NormalizeTitle(p),
		string(p.JobType),
		fold(p.Season),
	}, "|")
}
