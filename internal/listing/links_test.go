package listing

import "testing"

func TestJobLinkSameJob(t *testing.T) {
	// Each pair is one job as two sources spelled it.
	pairs := [][2]string{
		{"https://boards.greenhouse.io/figma/jobs/6241542004?gh_jid=6241542004",
			"https://job-boards.greenhouse.io/figma/jobs/6241542004?utm_source=Simplify&ref=Simplify"},
		{"https://www.hudsonrivertrading.com/careers/job/?gh_jid=8052083",
			"https://job-boards.greenhouse.io/wehrtyou/jobs/8052083"},
		{"https://nvidia.wd5.myworkdayjobs.com/en-US/nvidiaexternalcareersite/job/US-WA-Redmond/Compiler-Software-Engineering-Intern---Summer-2027_JR2028607",
			"https://nvidia.wd5.myworkdayjobs.com/nvidiaexternalcareersite/job/US-CA-Santa-Clara/Compiler-Intern_JR2028607/"},
		{"https://www.amazon.jobs/jobs/10587999/apply", "https://amazon.jobs/en/jobs/10587999/software-dev-intern"},
		{"https://jobs.ashbyhq.com/notion/3fba1c39-c5cb-47d7-9ad2-1cec4d7e9d0c",
			"https://jobs.ashbyhq.com/notion/3fba1c39-c5cb-47d7-9ad2-1cec4d7e9d0c/application?utm_source=x"},
		// Any other site: lowercase host, no www, no trailing slash, no tracking.
		{"https://WWW.Stripe.com/jobs/listing/swe-intern/7012345/?utm_campaign=x",
			"https://stripe.com/jobs/listing/swe-intern/7012345"},
	}
	for _, p := range pairs {
		a, okA := JobLink(p[0])
		b, okB := JobLink(p[1])
		if !okA || !okB || a != b {
			t.Errorf("should be the same job:\n %s -> %q %v\n %s -> %q %v", p[0], a, okA, p[1], b, okB)
		}
	}
}

func TestJobLinkDifferentJobs(t *testing.T) {
	// Same company tenant, different requisition: different jobs.
	a, _ := JobLink("https://nvidia.wd5.myworkdayjobs.com/x/job/A/Intern_JR1")
	b, _ := JobLink("https://nvidia.wd5.myworkdayjobs.com/x/job/A/Intern_JR2")
	if a == b {
		t.Errorf("different requisitions got the same identity %q", a)
	}
	// Same requisition number at two companies: different jobs.
	c, _ := JobLink("https://acme.wd1.myworkdayjobs.com/x/job/A/Intern_R100")
	d, _ := JobLink("https://other.wd1.myworkdayjobs.com/x/job/A/Intern_R100")
	if c == d {
		t.Errorf("different companies got the same identity %q", c)
	}
}

func TestJobLinkGenericLinksNeverMatch(t *testing.T) {
	for _, u := range []string{
		"https://stripe.com/careers",
		"https://www.citadel.com/careers/details/software-engineer-intern-us/",
		"https://job-boards.greenhouse.io/figma", // a board, not a job
		"not a url",
		"",
	} {
		if id, ok := JobLink(u); ok {
			t.Errorf("JobLink(%q) = %q, want no identity (generic link)", u, id)
		}
	}
}

func TestSeasonFromTitle(t *testing.T) {
	tests := map[string]string{
		"Software Engineer Intern - Summer 2027":     "Summer 2027",
		"AI Research Scientist Intern - 2027 Summer": "Summer 2027",
		"Data Intern (Fall '27)":                     "Fall 2027",
		"Software Engineer - Intern - 2027 Start":    "", // a year alone isn't a season
		"Summer Analyst Intern":                      "", // a season alone isn't either
	}
	for title, want := range tests {
		if got := SeasonFromTitle(title); got != want {
			t.Errorf("SeasonFromTitle(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestMergeDegree(t *testing.T) {
	if MergeDegree(DegreeUnknown, AdvancedRequired) != AdvancedRequired {
		t.Error("unknown must not override a known level")
	}
	if MergeDegree(AdvancedRequired, BachelorsOK) != BachelorsOK {
		t.Error("any posting open to bachelor's makes the job open to bachelor's")
	}
}
