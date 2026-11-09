package listing

import (
	"regexp"
	"slices"
	"strings"
)

// --- Role tags ---------------------------------------------------------------

// Role is what kind of work a job is. A listing can have several roles.
type Role string

const (
	Backend  Role = "backend"
	Frontend Role = "frontend"
	ML       Role = "ml"
	Data     Role = "data"
	PM       Role = "pm"
	Quant    Role = "quant"
	Hardware Role = "hardware"
	// AI: the title mentions AI but no ML-specific term ("AI Automation &
	// Revenue Ops"). Kept apart from ML so ML stays precise (decided 2026-11-08).
	AI         Role = "ai"
	GeneralSWE Role = "general_swe" // only when nothing more specific applies
)

// AllRoles lists every role, in the order the filter bar shows them.
var AllRoles = []Role{GeneralSWE, Backend, Frontend, ML, AI, Data, PM, Quant, Hardware}

// RoleLabels are the names people see.
var RoleLabels = map[Role]string{
	GeneralSWE: "General SWE", Backend: "Backend", Frontend: "Frontend", ML: "ML", AI: "AI",
	Data: "Data", PM: "PM", Quant: "Quant", Hardware: "Hardware",
}

// sectionRoles: a source's own section is a strong signal. Simplify's
// "AI/ML/Data" section isn't here: it mixes ML engineers with data analysts,
// so for those roles the title decides between ML and Data.
var sectionRoles = map[string]Role{
	"quant":    Quant,    // Simplify, SpeedyApply
	"hardware": Hardware, // Simplify
}

// productSection is Simplify's "Product" section. It is broader than
// product management (marketing, payments, operations...), so it gives PM
// only when the title also says "product" and isn't another product job.
const productSection = "product"

var (
	productWord     = words(`product`)
	otherProductJob = words(`product (?:marketing|design|designer|operations|specialist|analyst)`)
)

// aiMLDataSection is Simplify's mixed section, folded.
const aiMLDataSection = "ai ml data"

// titleRoles are whole-word phrases matched on the folded title
// ("Front-End" folds to "front end"). Each was checked against real titles;
// see tags_test.go for the ones that must NOT match.
var titleRoles = []struct {
	role    Role
	pattern *regexp.Regexp
}{
	{Backend, words(`backend|back end|full ?stack`)},
	{Frontend, words(`frontend|front end|full ?stack|ui engineer|web developer`)},
	// ML needs an ML-specific term; "AI" alone is the AI tag (below).
	{ML, words(`machine learning|ml|deep learning|reinforcement learning|computer vision|nlp|natural language processing|llms?|large language models?|mlops|genai|generative ai|neural networks?`)},
	{Data, words(`data (?:analyst|analysts|analytics|science|scientist|scientists|engineer|engineering)|business intelligence|analytics`)},
	{PM, words(`product manager|product management|apm|associate product manager|technical product manager`)},
	{Quant, words(`quant|quantitative|quants`)},
	{Hardware, words(`hardware|electrical|asic|fpga|pcb|analog|circuit|silicon|rf engineer|mechanical`)},
}

// aiWord is "AI" as a word ("ai engineer", "applied ai"), not inside
// another word ("retail", "maintain").
var aiWord = words(`ai|artificial intelligence|agentic`)

// words compiles a whole-word pattern: "ml" matches "ml engineer", never "html".
func words(alternatives string) *regexp.Regexp {
	return regexp.MustCompile(`\b(?:` + alternatives + `)\b`)
}

// Roles returns the role tags for one posting, from its title and the
// source's section ("" if the source has none). With no clear signal it
// is [GeneralSWE], since both sources are SWE lists, except in Simplify's
// Product section, which isn't SWE: there it can be empty (no tag beats a
// wrong one; the listing still shows when no role filter is on).
func Roles(title, section string) []Role {
	t := fold(title)
	sec := fold(section)

	var roles []Role
	add := func(r Role) {
		if !slices.Contains(roles, r) {
			roles = append(roles, r)
		}
	}
	if r, ok := sectionRoles[sec]; ok {
		add(r)
	}
	if sec == productSection && productWord.MatchString(t) && !otherProductJob.MatchString(t) {
		add(PM)
	}
	for _, tr := range titleRoles {
		if tr.pattern.MatchString(t) {
			add(tr.role)
		}
	}
	// In the AI/ML/Data section with no ML or data word in the title
	// ("Research Intern"), the section still says it's data or ML work.
	// Data is the safer of the two: it doesn't claim ML engineering.
	// "AI" without an ML term: the AI tag, never ML.
	if !slices.Contains(roles, ML) && aiWord.MatchString(t) {
		add(AI)
	}
	if sec == aiMLDataSection && !slices.Contains(roles, ML) && !slices.Contains(roles, Data) && !slices.Contains(roles, AI) {
		add(Data)
	}
	if len(roles) == 0 && sec != productSection {
		add(GeneralSWE)
	}
	slices.SortFunc(roles, func(a, b Role) int { return slices.Index(AllRoles, a) - slices.Index(AllRoles, b) })
	return roles
}

// --- Industry tags -----------------------------------------------------------

// Industry is a company's field. Tagged per company, so every listing at
// that company shares it.
type Industry string

const Healthcare Industry = "healthcare"

// IndustryLabels are the names people see.
var IndustryLabels = map[Industry]string{Healthcare: "Healthcare"}

// industryRule decides one industry from a company name. To add an
// industry: add a rule here, a label above, and a row to the industries
// table (a migration). Nothing else changes.
type industryRule struct {
	industry Industry
	// always: normalized company names that are in this industry even though
	// no keyword says so ("athenahealth" is one word, so "health" misses it).
	always map[string]bool
	// never: normalized names a keyword would wrongly tag. Checked first.
	never map[string]bool
	// approved: names a keyword tagged and a person confirmed as correct, so
	// the industry review stops listing them. Approval doesn't tag anything
	// by itself: the keyword must still match.
	approved map[string]bool
	// keywords: whole words in the normalized company name.
	keywords *regexp.Regexp
}

// industryRules is hand-maintained. Precision over recall: a missing tag is
// better than a wrong one. cmd/ingest prints an industry review of keyword
// matches nobody has checked yet; move each one to "approved" (correct) or
// "never" (wrong), and it won't be listed again.
var industryRules = []industryRule{{
	industry: Healthcare,
	always: set(
		"athenahealth", "epic systems", "medtronic", "stryker", "abbott", "pfizer", "merck",
		"genentech", "moderna", "regeneron", "vertex", "intuitive surgical", "boston scientific",
		"baxter", "becton dickinson", "illumina", "insulet", "dexcom", "zimmer biomet holdings",
		"johnson and johnson", "eli lilly", "bristol myers squibb", "zocdoc", "tempus ai",
	),
	never: set(
		"medical properties trust", // a real-estate trust
		"healthpeak properties",    // a real-estate trust
	),
	approved: set( // reviewed 2026-11-08
		"cardinal health", "cook medical", "courier health", "cvs health",
		"fresenius medical care north america", "garner health", "ge healthcare",
		"health care service", "highmark health", "mayo clinic", "modernizing medicine",
		"molina healthcare", "regeneron pharmaceuticals", "universal health services",
		"vertex pharmaceuticals", "zus health",
	),
	keywords: words(`health|healthcare|medical|medicine|pharma|pharmaceutical|pharmaceuticals|hospital|hospitals|clinic|therapeutics|biosciences`),
}}

func set(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

// IndustryMatch is one industry tag and whether a person should check it.
type IndustryMatch struct {
	Industry Industry
	// NeedsReview: only a keyword matched, and the name isn't on the
	// approved list yet. These are what the industry review shows.
	NeedsReview bool
}

// Industries returns the industry tags for a company name.
func Industries(company string) []IndustryMatch {
	name := NormalizeCompany(company)
	var out []IndustryMatch
	for _, r := range industryRules {
		switch {
		case r.never[name]:
		case r.always[name]:
			out = append(out, IndustryMatch{Industry: r.industry})
		case r.keywords.MatchString(name):
			out = append(out, IndustryMatch{Industry: r.industry, NeedsReview: !r.approved[name]})
		}
	}
	return out
}

// RoleStrings converts roles for storage.
func RoleStrings(roles []Role) []string {
	out := make([]string, len(roles))
	for i, r := range roles {
		out[i] = string(r)
	}
	return out
}

// RoleLabel returns a role's display name ("ml" -> "ML").
func RoleLabel(r string) string {
	if l, ok := RoleLabels[Role(r)]; ok {
		return l
	}
	return strings.ReplaceAll(r, "_", " ")
}
