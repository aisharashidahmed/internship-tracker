package web

import (
	"net/http"
	"sync"
	"time"
)

// Times are stored as absolute moments; showing one (and deciding what
// "today" is) needs a timezone. Logged-in users have their own, reported by
// their browser; logged-out visitors get the server's default.

var (
	locMu    sync.Mutex
	locCache = map[string]*time.Location{}
)

// loadLocation turns an IANA name into a *time.Location, remembering the
// result. ok is false for an unknown name.
func loadLocation(name string) (*time.Location, bool) {
	if name == "" || name == "Local" { // "Local" would mean the server's zone, not the user's
		return nil, false
	}
	locMu.Lock()
	defer locMu.Unlock()
	if loc, ok := locCache[name]; ok {
		return loc, true
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, false
	}
	locCache[name] = loc
	return loc, true
}

// location is the timezone to show this request's times in.
func (s *Server) location(r *http.Request) *time.Location {
	if u := currentUser(r); u != nil {
		if loc, ok := loadLocation(u.Timezone); ok {
			return loc
		}
	}
	return s.defaultLoc
}

// formTimezone reads the timezone the browser put in a login or sign-up
// form (see auth.html), or "" if it's missing or not a real zone.
func formTimezone(r *http.Request) string {
	tz := r.PostFormValue("tz")
	if _, ok := loadLocation(tz); !ok {
		return ""
	}
	return tz
}
