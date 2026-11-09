package listing

import (
	"strings"
	"testing"
)

func TestNormalizeCompany(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Stripe", "stripe"},
		{"Stripe, Inc.", "stripe"},
		{"STRIPE INC", "stripe"},
		{"Johnson & Johnson", "johnson and johnson"},
		{"Susquehanna International Group (SIG)", "susquehanna international group"}, // nickname dropped
		{"Acme Holdings Co., Ltd.", "acme holdings"},
		{"Co", "co"},                     // never strip the whole name
		{"AtkinsRéalis", "atkinsrealis"}, // accents dropped, not split into two words
		{"Facebook", "meta"},             // alias table
		{"Meta Platforms, Inc.", "meta"},
		{"Metalenz", "metalenz"}, // alias must match the whole name, not a prefix
	}
	for _, tt := range tests {
		if got := NormalizeCompany(tt.in); got != tt.want {
			t.Errorf("NormalizeCompany(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNormalizeTitle(t *testing.T) {
	tests := []struct {
		title, season string
		locations     []string
		want          string
	}{
		{"Software Engineering Intern – Summer 2027", "Summer 2027", nil, "software engineer intern"},
		{"SWE Intern (Remote)", "Summer 2027", []string{"Remote in USA"}, "software engineer intern"},
		{"AI Research Scientist Intern - 2027 Summer", "Summer 2027", nil, "ai research scientist intern"},
		{"Waymo 2027 Intern", "Summer 2027", nil, "waymo intern"}, // a bare year goes
		{"Software Engineer Intern 🚀", "Summer 2027", nil, "software engineer intern"},
		{"Quant Intern - New York", "Summer 2027", []string{"New York, NY"}, "quant intern"},
		{"Quant Intern - NYC", "Summer 2027", []string{"New York, NY"}, "quant intern"},
		{"Quant Intern, New York, NY", "Summer 2027", []string{"NYC"}, "quant intern"},
		// The title's season disagrees with the posting's: keep it (seen at RTX).
		{"Software Engineer Intern - Spring 2027", "Summer 2027", nil, "software engineer intern spring 2027"},
		{"Data Intern - Autumn 2027", "Fall 2027", nil, "data intern"},
		// "Summer" without a year is part of the job's name; keep it.
		{"Summer Analyst Intern", "Summer 2027", nil, "summer analyst intern"},
		// A team name is kept.
		{"SWE Intern, Infrastructure", "Summer 2027", []string{"Seattle, WA"}, "software engineer intern infrastructure"},
		// A city that is NOT this posting's location is kept: it may be a team.
		{"Analyst Intern - Chicago", "Summer 2027", []string{"New York, NY"}, "analyst intern chicago"},
		// Never strip everything.
		{"Remote", "Summer 2027", nil, "remote"},
	}
	for _, tt := range tests {
		p := Posting{Title: tt.title, Season: tt.season, Locations: tt.locations}
		if got := NormalizeTitle(p); got != tt.want {
			t.Errorf("NormalizeTitle(%q, %s, %q) = %q, want %q", tt.title, tt.season, tt.locations, got, tt.want)
		}
	}
}

func TestNormalizeLocations(t *testing.T) {
	got := NormalizeLocations([]string{"NYC", "New York, NY", "New York City, NY", "Remote in USA", "Remote", "Ardmore,  PA", "LA"})
	want := []string{"New York, NY", "Remote", "Ardmore, PA", "Los Angeles, CA"}
	if strings.Join(got, ";") != strings.Join(want, ";") {
		t.Errorf("NormalizeLocations = %q, want %q", got, want)
	}
}

// posting builds a Summer 2027 internship posting for key tests.
func posting(company, title string, locations ...string) Posting {
	return Posting{Company: company, Title: title, JobType: Internship,
		Season: "Summer 2027", Locations: locations}
}

func TestDedupeKeyMerges(t *testing.T) {
	pairs := []struct{ a, b Posting }{
		{posting("Stripe", "Software Engineer Intern"), posting("Stripe, Inc.", "Software Engineer - Intern")},
		{posting("Stripe", "Software Engineering Intern – Summer 2027"), posting("Stripe", "SWE Intern (Remote)", "Remote")},
		{posting("Stripe", "Software Engineer – Intern"), posting("Stripe", "Software Engineer - Intern")}, // en dash = hyphen
		{posting("Facebook", "Data Science Intern"), posting("Meta", "Data Science Intern")},
	}
	for _, p := range pairs {
		if a, b := DedupeKey(p.a), DedupeKey(p.b); a != b {
			t.Errorf("should merge, got different keys:\n %q\n %q", a, b)
		}
	}
}

func TestRoleKeyIgnoresSeason(t *testing.T) {
	summer := posting("Tesla", "Software Engineer Intern - Summer 2027")
	spring := posting("Tesla", "Software Engineer Intern - Spring 2027")
	spring.Season = "Spring 2027"
	if RoleKey(summer) != RoleKey(spring) {
		t.Errorf("one role in two seasons got different role keys:\n %q\n %q", RoleKey(summer), RoleKey(spring))
	}
	if DedupeKey(summer) == DedupeKey(spring) {
		t.Error("but they must stay separate listings")
	}
}

func TestDedupeKeyKeepsApart(t *testing.T) {
	fall := posting("Stripe", "Software Engineer Intern")
	fall.Season = "Fall 2027"
	newGrad := posting("Stripe", "Software Engineer Intern")
	newGrad.JobType = NewGrad

	pairs := []struct {
		why  string
		a, b Posting
	}{
		{"different teams", posting("Stripe", "SWE Intern, Infrastructure"), posting("Stripe", "SWE Intern, Mobile")},
		{"different seasons", posting("Stripe", "Software Engineer Intern"), fall},
		{"different job types", posting("Stripe", "Software Engineer Intern"), newGrad},
		{"different companies", posting("Stripe", "Software Engineer Intern"), posting("Square", "Software Engineer Intern")},
		{"different degree tracks", posting("Citadel", "Quantitative Research Intern - PhD: Summer 2027"),
			posting("Citadel", "Quantitative Research Intern - Master's: Summer 2027")},
	}
	for _, p := range pairs {
		if DedupeKey(p.a) == DedupeKey(p.b) {
			t.Errorf("%s: should stay separate, both got %q", p.why, DedupeKey(p.a))
		}
	}
}

func TestGroupMergesSameJob(t *testing.T) {
	postings := []Posting{
		{SourceListingID: "1", Company: "SIG", Title: "Quant Intern", JobType: Internship,
			Season: "Summer 2027", Locations: []string{"Ardmore, PA"}, DegreeLevel: AdvancedRequired},
		{SourceListingID: "2", Company: "Stripe", Title: "SWE Intern", JobType: Internship,
			Season: "Summer 2027"},
		{SourceListingID: "3", Company: "SIG", Title: "Quant Intern", JobType: Internship,
			Season: "Summer 2027", Locations: []string{"London, UK"}, DegreeLevel: BachelorsOK},
	}

	got := Group(postings)

	if len(got) != 2 {
		t.Fatalf("got %d listings, want 2", len(got))
	}
	sig := got[0]
	if len(sig.Postings) != 2 {
		t.Errorf("SIG listing has %d postings, want 2 (one per office)", len(sig.Postings))
	}
	if sig.DegreeLevel != BachelorsOK {
		t.Errorf("SIG degree level = %s, want %s (any posting allowing bachelor's wins)",
			sig.DegreeLevel, BachelorsOK)
	}
}
