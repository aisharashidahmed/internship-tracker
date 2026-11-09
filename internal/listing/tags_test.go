package listing

import (
	"slices"
	"testing"
)

func TestRoles(t *testing.T) {
	tests := []struct {
		title, section string
		want           []Role
	}{
		// From the title.
		{"Backend Software Engineer Intern", "Software", []Role{Backend}},
		{"Front-End Engineer Intern", "", []Role{Frontend}},
		{"Fullstack Software Engineer Intern", "", []Role{Backend, Frontend}},
		{"Machine Learning Engineer Intern", "", []Role{ML}},
		{"ML Platform Intern", "", []Role{ML}},
		{"Associate Product Manager Intern", "", []Role{PM}},
		{"APM Intern", "", []Role{PM}},
		{"Quantitative Researcher Intern", "", []Role{Quant}},
		// From the source's section.
		{"Trading Intern", "Quant", []Role{Quant}},
		{"Electrical Engineering Intern", "Hardware", []Role{Hardware}},
		{"Product Strategy Intern", "Product", []Role{PM}},
		{"Product Intern", "Product", []Role{PM}},
		// Simplify's Product section holds non-PM jobs too: no tag at all,
		// rather than a wrong PM or General SWE.
		{"Product Marketing Intern", "Product", nil},
		{"Enterprise Payments Intern", "Product", nil},
		{"Product Operations Intern", "Product", nil},
		// Simplify's mixed AI/ML/Data section: the title picks ML or Data.
		{"Data Analyst Intern", "AI/ML/Data", []Role{Data}},
		{"AI Research Scientist Intern", "AI/ML/Data", []Role{ML}},
		{"ML Data Scientist Intern", "AI/ML/Data", []Role{ML, Data}},
		{"Research Intern", "AI/ML/Data", []Role{Data}},
		// No signal: General SWE.
		{"Software Engineer Intern", "Software", []Role{GeneralSWE}},
		{"Software Engineer Intern", "", []Role{GeneralSWE}},
	}
	for _, tt := range tests {
		if got := Roles(tt.title, tt.section); !slices.Equal(got, tt.want) {
			t.Errorf("Roles(%q, %q) = %v, want %v", tt.title, tt.section, got, tt.want)
		}
	}
}

// Titles that contain a tag's letters or a similar word, but must NOT get it.
func TestRolesMustNotTag(t *testing.T) {
	tests := []struct {
		title string
		not   Role
	}{
		{"HTML Email Developer Intern", ML},              // "ml" inside "html"
		{"Retail Operations Intern", ML},                 // "ai" inside "retail"
		{"Front Office Analyst Intern", Frontend},        // front office is not front end
		{"Back Office Operations Intern", Backend},       // nor back office
		{"Product Development Engineer Intern", PM},      // product, but not management
		{"Product Designer Intern", PM},                  //
		{"Program Manager Intern", PM},                   // program, not product
		{"Quantum Computing Intern", Quant},              // quantum is not quant
		{"Data Center Technician Intern", Data},          // data center is not data work
		{"Software Engineer Intern - Data Platform", ML}, // data, not ML
	}
	for _, tt := range tests {
		if got := Roles(tt.title, ""); slices.Contains(got, tt.not) {
			t.Errorf("Roles(%q) = %v, must not include %s", tt.title, got, tt.not)
		}
	}
}

func TestIndustries(t *testing.T) {
	tests := []struct {
		company     string
		want        bool // tagged Healthcare?
		needsReview bool
	}{
		{"Acme Health", true, true},
		{"Acme Healthcare", true, true},
		{"Acme Medicine", true, true},
		{"Acme Pharmaceuticals", true, true},
		{"Acme Clinic", true, true},
		{"CVS Health", true, false},                // keyword match, already approved
		{"athenahealth", true, false},              // one word: only the always-list catches it
		{"Medtronic", true, false},                 // no keyword at all
		{"Medical Properties Trust", false, false}, // never-list beats the keyword
		{"Healthpeak Properties", false, false},    // "healthpeak" is not the word "health"
		{"Healthy Snacks Co", false, false},        // "healthy" is not "health"
		{"Stripe", false, false},
	}
	for _, tt := range tests {
		got := Industries(tt.company)
		tagged := len(got) == 1 && got[0].Industry == Healthcare
		if tagged != tt.want || (tagged && got[0].NeedsReview != tt.needsReview) {
			t.Errorf("Industries(%q) = %+v, want healthcare=%v needsReview=%v", tt.company, got, tt.want, tt.needsReview)
		}
	}
}

func TestIndustries_ApprovedNamesLeaveTheReview(t *testing.T) {
	r := industryRules[0]
	saved := r.approved
	defer func() { industryRules[0].approved = saved }()

	industryRules[0].approved = set("cvs health")
	if got := Industries("CVS Health"); len(got) != 1 || got[0].NeedsReview {
		t.Errorf("approved CVS Health = %+v, want tagged and not needing review", got)
	}
	if got := Industries("Cardinal Health"); len(got) != 1 || !got[0].NeedsReview {
		t.Errorf("unreviewed Cardinal Health = %+v, want tagged and needing review", got)
	}
	industryRules[0].approved = set("stripe")
	if got := Industries("Stripe"); len(got) != 0 {
		t.Errorf("approval without a keyword match must not tag: %+v", got)
	}
}
