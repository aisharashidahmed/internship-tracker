// Package linkcheck checks apply links to notice jobs that were taken
// down before a source says so. Only clear signals count as "dead";
// anything ambiguous is "couldn't tell" and never closes a listing.
//
// What the job boards do was checked by hand in November 2026:
//   - Greenhouse: a removed job redirects to the company's board,
//     usually with ?error=true; a live one returns 200.
//   - Ashby: a removed job returns 404.
//   - Workday: every job page is a JavaScript app returning 200, so a
//     200 there says nothing.
package linkcheck

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"internship-tracker/internal/store"
)

// Verdict is what one response tells us.
type Verdict struct {
	Result store.LinkResult
	Detail string   // shown in logs and kept with the posting, e.g. "HTTP 404"
	Next   *url.URL // set when we should follow a redirect instead
}

var greenhouseJobID = regexp.MustCompile(`/jobs/(\d+)`)

// Classify decides what a response to req means.
func Classify(req *url.URL, status int, location string) Verdict {
	host := strings.ToLower(req.Hostname())

	switch {
	case status == http.StatusNotFound || status == http.StatusGone:
		return Verdict{Result: store.LinkDead, Detail: fmt.Sprintf("HTTP %d", status)}

	case status >= 300 && status < 400 && location != "":
		next, err := req.Parse(location)
		if err != nil {
			return Verdict{Result: store.LinkUnknown, Detail: "unreadable redirect"}
		}
		if strings.HasSuffix(host, "greenhouse.io") {
			if id := greenhouseJobID.FindStringSubmatch(req.Path); id != nil &&
				(!strings.Contains(next.String(), id[1]) || next.Query().Get("error") == "true") {
				return Verdict{Result: store.LinkDead, Detail: "Greenhouse redirected away from the job"}
			}
		}
		return Verdict{Next: next}

	case status >= 200 && status < 300:
		if strings.HasSuffix(host, "myworkdayjobs.com") {
			return Verdict{Result: store.LinkUnknown, Detail: "Workday pages need JavaScript"}
		}
		return Verdict{Result: store.LinkAlive, Detail: fmt.Sprintf("HTTP %d", status)}

	default: // 403/429 (often bot blocking), 5xx (outages), odd codes
		return Verdict{Result: store.LinkUnknown, Detail: fmt.Sprintf("HTTP %d", status)}
	}
}
