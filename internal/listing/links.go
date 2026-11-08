package listing

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// JobLink returns an apply link's job identity: two links with the same
// identity open the same job ad, however each source spelled the URL.
// ok is false for generic links ("stripe.com/careers"), which name no
// specific job and must never be used for matching.
//
//	https://boards.greenhouse.io/figma/jobs/6241542004?gh_jid=6241542004  -> "greenhouse:6241542004"
//	https://job-boards.greenhouse.io/figma/jobs/6241542004?utm_source=x  -> "greenhouse:6241542004"
//	https://stripe.com/jobs/listing/swe-intern/7012345/                  -> "stripe.com/jobs/listing/swe-intern/7012345"
func JobLink(raw string) (identity string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	path := strings.TrimRight(u.Path, "/")

	// Company sites that embed a Greenhouse board carry its job id in
	// ?gh_jid=, e.g. hudsonrivertrading.com/careers/job/?gh_jid=8052083.
	if id := u.Query().Get("gh_jid"); id != "" {
		return "greenhouse:" + id, true
	}

	// Job boards whose URLs embed the job id in a known place. Matching on
	// the id alone ignores parts that sources spell differently: the board
	// subdomain, locale ("/en-US"), or a title slug that changes on edits.
	for _, ats := range jobBoards {
		if ats.host.MatchString(host) {
			if id := ats.id(u, host, path); id != "" {
				return ats.name + ":" + id, true
			}
			break // a known board, but not a job page (e.g. the board's home)
		}
	}

	// Any other site: the canonical URL is the identity, but only if it
	// contains something that looks like a job id.
	canonical := host + path
	if q := cleanQuery(u.Query()); q != "" {
		canonical += "?" + q
	}
	if !jobID.MatchString(path + "?" + u.RawQuery) {
		return "", false
	}
	return canonical, true
}

// jobID is a run of 5+ digits or a UUID: what an id inside a URL looks like.
var jobID = regexp.MustCompile(`\d{5,}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

type jobBoard struct {
	name string
	host *regexp.Regexp
	id   func(u *url.URL, host, path string) string
}

var jobBoards = []jobBoard{
	{"greenhouse", regexp.MustCompile(`(^|\.)greenhouse\.io$`), func(u *url.URL, _, path string) string {
		// Greenhouse job ids are global, so the company board name isn't needed.
		return capture(numericJobPath, path)
	}},
	{"lever", regexp.MustCompile(`^jobs\.lever\.co$`), func(_ *url.URL, _, path string) string {
		return capture(companyThenUUID, path)
	}},
	{"ashby", regexp.MustCompile(`^jobs\.ashbyhq\.com$`), func(_ *url.URL, _, path string) string {
		return capture(companyThenUUID, path)
	}},
	{"workday", regexp.MustCompile(`\.myworkdayjobs\.com$`), func(_ *url.URL, host, path string) string {
		// .../job/<City>/<Title-Slug>_R0020843 : the requisition id after the
		// last "_" is unique within one company (the first part of the host).
		req := capture(workdayRequisition, path)
		if req == "" {
			return ""
		}
		return strings.Split(host, ".")[0] + ":" + req
	}},
	{"amazon", regexp.MustCompile(`^amazon\.jobs$`), func(_ *url.URL, _, path string) string {
		return capture(numericJobPath, path)
	}},
}

var (
	numericJobPath     = regexp.MustCompile(`/jobs/(\d+)`)                 // /jobs/7012345
	companyThenUUID    = regexp.MustCompile(`^/[^/]+/([0-9a-f-]{36})`)     // /notion/3fba1c39-...
	workdayRequisition = regexp.MustCompile(`/job/[^?]*_([A-Za-z0-9-]+)$`) // .../Title_R0020843
)

// capture returns re's first capture group in s, or "" if re doesn't match.
func capture(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return m[1]
}

// trackingParams say who sent the visitor, not which job it is.
var trackingParams = map[string]bool{
	"ref": true, "source": true, "src": true, "gh_src": true, "lever-source": true,
	"source_id": true, "referrer": true,
}

// cleanQuery drops tracking parameters and sorts the rest, so that
// "?b=2&a=1&utm_source=x" and "?a=1&b=2" compare equal.
func cleanQuery(q url.Values) string {
	var keys []string
	for k := range q {
		if strings.HasPrefix(strings.ToLower(k), "utm_") || trackingParams[strings.ToLower(k)] {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		for _, v := range q[k] {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}
