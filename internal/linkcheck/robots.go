package linkcheck

import (
	"bufio"
	"strings"
)

// Robots is the part of a site's robots.txt that applies to us
// (RFC 9309): the rules for our user agent if the file names it,
// otherwise the rules for "*".
type Robots struct {
	rules []rule
}

type rule struct {
	allow   bool
	pattern string // path prefix; "*" matches anything, a final "$" anchors the end
}

// AllowAll and DisallowAll are used when robots.txt can't be read: a
// missing file (4xx) allows everything; an error or 5xx means "assume
// nothing is allowed" until we can read it, as RFC 9309 asks.
var (
	AllowAll    = &Robots{}
	DisallowAll = &Robots{rules: []rule{{allow: false, pattern: "/"}}}
)

// ParseRobots reads a robots.txt body for the given product token
// (e.g. "internship-tracker-linkcheck").
func ParseRobots(body, agent string) *Robots {
	agent = strings.ToLower(agent)
	var mine, star []rule
	var groupAgents []string
	inRules := false // a User-agent line after rules starts a new group

	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)

		switch key {
		case "user-agent":
			if inRules {
				groupAgents, inRules = nil, false
			}
			groupAgents = append(groupAgents, strings.ToLower(value))
		case "allow", "disallow":
			inRules = true
			if key == "disallow" && value == "" {
				continue // "Disallow:" with nothing means allow everything
			}
			r := rule{allow: key == "allow", pattern: value}
			for _, a := range groupAgents {
				switch {
				case a == "*":
					star = append(star, r)
				case strings.HasPrefix(agent, a) || strings.Contains(agent, a):
					mine = append(mine, r)
				}
			}
		}
	}
	if mine != nil {
		return &Robots{rules: mine}
	}
	return &Robots{rules: star}
}

// Allowed reports whether we may fetch path (with its query). The
// longest matching rule wins; on a tie, Allow wins; no match means allowed.
func (r *Robots) Allowed(path string) bool {
	best, allowed := -1, true
	for _, ru := range r.rules {
		if n, ok := match(ru.pattern, path); ok && (n > best || (n == best && ru.allow)) {
			best, allowed = n, ru.allow
		}
	}
	return allowed
}

// match reports whether pattern matches the start of path, and how long
// the pattern is (its "specificity").
func match(pattern, path string) (int, bool) {
	anchored := strings.HasSuffix(pattern, "$")
	p := strings.TrimSuffix(pattern, "$")
	parts := strings.Split(p, "*")
	pos := 0
	for i, part := range parts {
		if i == 0 {
			if !strings.HasPrefix(path, part) {
				return 0, false
			}
			pos = len(part)
			continue
		}
		j := strings.Index(path[pos:], part)
		if j < 0 {
			return 0, false
		}
		pos += j + len(part)
	}
	if anchored && pos != len(path) && !strings.HasSuffix(p, "*") {
		return 0, false
	}
	return len(pattern), true
}
