package linkcheck

import (
	"net/url"
	"testing"

	"internship-tracker/internal/store"
)

func TestRobots(t *testing.T) {
	body := `
# a comment
User-agent: Bytespider
Disallow: /

User-agent: *
Disallow: /api/
Disallow: /private
Allow: /private/jobs/
Disallow: /*.pdf$
Disallow:
`
	r := ParseRobots(body, "internship-tracker-linkcheck")
	tests := map[string]bool{
		"/stripe/jobs/123":     true,
		"/api/jobs":            false,
		"/private/x":           false,
		"/private/jobs/42":     true, // longer Allow beats shorter Disallow
		"/files/offer.pdf":     false,
		"/files/offer.pdf?x=1": true, // "$" anchors the end
	}
	for path, want := range tests {
		if got := r.Allowed(path); got != want {
			t.Errorf("Allowed(%q) = %v, want %v", path, got, want)
		}
	}

	mine := ParseRobots("User-agent: *\nAllow: /\n\nUser-agent: internship-tracker-linkcheck\nDisallow: /\n", "internship-tracker-linkcheck")
	if mine.Allowed("/anything") {
		t.Error("a group naming our agent must win over *")
	}
	if !AllowAll.Allowed("/x") || DisallowAll.Allowed("/x") {
		t.Error("AllowAll / DisallowAll are wrong")
	}
}

func TestClassify(t *testing.T) {
	u := func(s string) *url.URL { v, _ := url.Parse(s); return v }
	gh := u("https://job-boards.greenhouse.io/figma/jobs/6241542004")

	tests := []struct {
		name     string
		req      *url.URL
		status   int
		location string
		want     store.LinkResult
		follow   bool
	}{
		{"404 anywhere", u("https://jobs.ashbyhq.com/x/3fba1c39-c5cb-47d7-9ad2-1cec4d7e9d0c"), 404, "", store.LinkDead, false},
		{"410 gone", u("https://example.com/jobs/12345"), 410, "", store.LinkDead, false},
		{"greenhouse removed job", gh, 302, "https://job-boards.greenhouse.io/figma?error=true", store.LinkDead, false},
		{"greenhouse redirect keeping the job", gh, 301, "/figma/jobs/6241542004?gh_src=x", "", true},
		{"live job", gh, 200, "", store.LinkAlive, false},
		{"workday always 200", u("https://nvidia.wd5.myworkdayjobs.com/x/job/A_JR1"), 200, "", store.LinkUnknown, false},
		{"bot blocking", gh, 403, "", store.LinkUnknown, false},
		{"rate limited", gh, 429, "", store.LinkUnknown, false},
		{"outage", gh, 503, "", store.LinkUnknown, false},
		{"other redirect: follow", u("https://example.com/jobs/12345"), 302, "https://example.com/careers/12345", "", true},
	}
	for _, tt := range tests {
		v := Classify(tt.req, tt.status, tt.location)
		if v.Result != tt.want || (v.Next != nil) != tt.follow {
			t.Errorf("%s: got %q next=%v, want %q follow=%v", tt.name, v.Result, v.Next, tt.want, tt.follow)
		}
	}
}

func TestSameSite(t *testing.T) {
	u := func(s string) *url.URL { v, _ := url.Parse(s); return v }
	tests := []struct {
		a, b string
		want bool
	}{
		{"https://boards.greenhouse.io/x/jobs/1", "https://job-boards.greenhouse.io/x/jobs/1", true},
		{"https://jobs.bytedance.com/x", "https://joinbytedance.com/x", false},
		{"https://a.example.co.uk/x", "https://b.other.co.uk/x", false}, // co.uk is a suffix, not a site
	}
	for _, tt := range tests {
		if got := sameSite(u(tt.a), u(tt.b)); got != tt.want {
			t.Errorf("sameSite(%s, %s) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
