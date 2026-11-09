package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"internship-tracker/internal/store"
)

// fakeAccounts keeps accounts in memory, behaving like the real store
// (whose own tests run against Postgres).
type fakeAccounts struct {
	mu       sync.Mutex
	newSince map[int64]time.Time // user id -> boundary; set by ViewFeed
	invites  map[string]int      // token -> seats left
	users    map[string]store.User
	sessions map[string]int64 // token -> user id
	nextID   int64
}

func newFakeAccounts() *fakeAccounts {
	return &fakeAccounts{invites: map[string]int{"good-invite": 1}, users: map[string]store.User{},
		sessions: map[string]int64{}, newSince: map[int64]time.Time{}}
}

func (f *fakeAccounts) InviteUsable(_ context.Context, t string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.invites[t] > 0, nil
}

func (f *fakeAccounts) SignUp(_ context.Context, invite, email, hash, tz string) (store.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if invite != "" && f.invites[invite] <= 0 { // open sign-up: an invite is optional
		return store.User{}, store.ErrInviteInvalid
	}
	if _, taken := f.users[strings.ToLower(email)]; taken {
		return store.User{}, store.ErrEmailTaken
	}
	if invite != "" {
		f.invites[invite]--
	}
	f.nextID++
	u := store.User{ID: f.nextID, Email: email, PasswordHash: hash, Timezone: tz}
	f.users[strings.ToLower(email)] = u
	return u, nil
}

func (f *fakeAccounts) UserByEmail(_ context.Context, email string) (store.User, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[strings.ToLower(email)]
	return u, ok, nil
}

func (f *fakeAccounts) CreateSession(_ context.Context, id int64) (string, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	token := "tok" + time.Now().Format(time.RFC3339Nano)
	f.sessions[token] = id
	return token, time.Now().Add(time.Hour), nil
}

func (f *fakeAccounts) SessionUser(_ context.Context, token string) (store.User, bool, bool, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.sessions[token]
	if !ok {
		return store.User{}, false, false, time.Time{}, nil
	}
	for _, u := range f.users {
		if u.ID == id {
			return store.User{ID: u.ID, Email: u.Email, Timezone: u.Timezone}, true, false, time.Now().Add(time.Hour), nil
		}
	}
	return store.User{}, false, false, time.Time{}, nil
}

func (f *fakeAccounts) SetTimezone(_ context.Context, id int64, tz string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, u := range f.users {
		if u.ID == id {
			u.Timezone = tz
			f.users[k] = u
		}
	}
	return nil
}

func (f *fakeAccounts) DeleteSession(_ context.Context, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, token)
	return nil
}

func (f *fakeAccounts) ViewFeed(_ context.Context, id int64) (time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.newSince[id]; !ok {
		f.newSince[id] = time.Now().Add(-store.FirstVisitWindow)
	}
	return f.newSince[id], nil
}

func (f *fakeAccounts) FeedNewSince(_ context.Context, id int64) (time.Time, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.newSince[id]
	return t, ok, nil
}

func (f *fakeAccounts) MarkAllSeen(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.newSince[id] = time.Now()
	return nil
}

// browser is a test client that keeps cookies and doesn't follow
// redirects, like a person's browser but easier to inspect.
type browser struct {
	t      *testing.T
	srv    *httptest.Server
	client *http.Client
}

func newBrowser(t *testing.T, accounts Accounts) *browser {
	return newBrowserWithFeed(t, fakeFeed{}, accounts)
}

func newBrowserWithFeed(t *testing.T, f FeedReader, accounts Accounts) *browser {
	t.Helper()
	s, err := New(f, accounts, newFakeTracker(), newFakeStars(), newFakeImports(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	return newBrowserFor(t, s)
}

func newBrowserFor(t *testing.T, s *Server) *browser {
	t.Helper()
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, srv: srv, client: &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (b *browser) get(path string) (int, string) {
	b.t.Helper()
	res, err := b.client.Get(b.srv.URL + path)
	if err != nil {
		b.t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body)
}

// post submits a form the way a browser on our own site does.
func (b *browser) post(path string, form url.Values) (int, string) {
	b.t.Helper()
	req, _ := http.NewRequest("POST", b.srv.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	res, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body)
}

func TestSignupLoginLogout(t *testing.T) {
	b := newBrowser(t, newFakeAccounts())

	if _, page := b.get("/signup?invite=good-invite"); !strings.Contains(page, "Create account") {
		t.Fatal("sign-up form not shown for a good invite")
	}
	status, _ := b.post("/signup", url.Values{"invite": {"good-invite"}, "email": {"aisha@college.edu"}, "password": {"correct horse battery"}})
	if status != http.StatusSeeOther {
		t.Fatalf("sign-up status %d, want a redirect home", status)
	}
	if _, page := b.get("/"); !strings.Contains(page, "aisha@college.edu") || !strings.Contains(page, "Log out") {
		t.Error("after sign-up the header doesn't show the logged-in email")
	}

	b.post("/logout", nil)
	if _, page := b.get("/"); strings.Contains(page, "aisha@college.edu") || !strings.Contains(page, "Log in") {
		t.Error("still logged in after logout")
	}

	status, page := b.post("/login", url.Values{"email": {"Aisha@College.edu"}, "password": {"wrong password!"}})
	if status != http.StatusUnauthorized || !strings.Contains(page, "Email or password is incorrect.") {
		t.Errorf("wrong password: status %d", status)
	}
	if status, _ := b.post("/login", url.Values{"email": {"Aisha@College.edu"}, "password": {"correct horse battery"}}); status != http.StatusSeeOther {
		t.Errorf("login status %d, want a redirect home", status)
	}
	if _, page := b.get("/"); !strings.Contains(page, "aisha@college.edu") {
		t.Error("not logged in after a correct login")
	}
}

func TestSignup_RefusesBadInput(t *testing.T) {
	accounts := newFakeAccounts()
	accounts.invites["good-invite"] = 5
	b := newBrowser(t, accounts)
	signup := func(invite, email, pw string) string {
		_, page := b.post("/signup", url.Values{"invite": {invite}, "email": {email}, "password": {pw}})
		return page
	}

	if page := signup("good-invite", "not-an-email", "correct horse battery"); !strings.Contains(page, "valid email") {
		t.Error("bad email not refused")
	}
	if page := signup("good-invite", "a@college.edu", "short"); !strings.Contains(page, "at least 10") {
		t.Error("short password not refused")
	}
	if page := signup("made-up", "a@college.edu", "correct horse battery"); !strings.Contains(page, "invalid, expired, or already used") {
		t.Error("bad invite not refused")
	}
	signup("good-invite", "a@college.edu", "correct horse battery")
	b.post("/logout", nil)
	page := signup("good-invite", "A@college.edu", "another password")
	if !strings.Contains(page, "couldn&#39;t create an account") || strings.Contains(page, "already registered") {
		t.Error("duplicate email should get the neutral message")
	}
	if _, page := b.get("/signup?invite=made-up"); !strings.Contains(page, "invalid, expired, or already used") {
		t.Error("a bad invite link should say so before asking for a password")
	}
}

func TestLogin_RateLimited(t *testing.T) {
	b := newBrowser(t, newFakeAccounts())
	for range 5 {
		b.post("/login", url.Values{"email": {"victim@college.edu"}, "password": {"guess guess guess"}})
	}
	status, page := b.post("/login", url.Values{"email": {"victim@college.edu"}, "password": {"guess guess guess"}})
	if status != http.StatusTooManyRequests || !strings.Contains(page, "Too many attempts") {
		t.Errorf("6th attempt: status %d, want 429", status)
	}
}

func TestCrossSiteFormPostIsRejected(t *testing.T) {
	b := newBrowser(t, newFakeAccounts())
	req, _ := http.NewRequest("POST", b.srv.URL+"/logout", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site") // what a browser sends for a post from another website
	res, err := b.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site post status %d, want 403", res.StatusCode)
	}
}

func TestNewSinceYouLastLooked(t *testing.T) {
	// Two listings: one appeared an hour ago (new), one three days ago (not).
	hourAgo, daysAgo := time.Now().Add(-time.Hour), time.Now().Add(-72*time.Hour)
	f := fakeFeed{listings: listings(2)}
	f.listings[0].Title, f.listings[0].NewAt = "Fresh Intern", &hourAgo
	f.listings[1].Title, f.listings[1].NewAt = "Older Intern", &daysAgo

	// Logged out: no New badges, no count.
	logged := newBrowserWithFeed(t, f, newFakeAccounts())
	if _, page := logged.get("/"); strings.Contains(page, `chip new`) || !strings.Contains(page, "log in</a> to track applications") {
		t.Error("logged out: should show no New badges and a log-in hint")
	}

	accounts := newFakeAccounts()
	b := newBrowserWithFeed(t, f, accounts)
	b.post("/signup", url.Values{"invite": {"good-invite"}, "email": {"a@college.edu"}, "password": {"correct horse battery"}})

	// First visit: the last 24 hours are new.
	_, page := b.get("/")
	if strings.Count(page, `class="chip new"`) != 1 || !strings.Contains(page, "<strong>1 new for you</strong>") {
		t.Error("first visit: want exactly the fresh listing marked New, and '1 new'")
	}
	if _, page := b.get("/?new=1"); strings.Contains(page, "Older Intern") || !strings.Contains(page, "Fresh Intern") {
		t.Error("Only new should hide the older listing")
	}

	// Mark all as seen, coming back to the same filtered view.
	status, _ := b.post("/seen", url.Values{"back": {"/?new=1&type=internship"}})
	if status != http.StatusSeeOther {
		t.Fatalf("mark all as seen: status %d", status)
	}
	if _, page := b.get("/"); strings.Contains(page, `class="chip new"`) || !strings.Contains(page, "<strong>0 new for you</strong>") {
		t.Error("after Mark all as seen, nothing should be New")
	}
}

func TestMarkAllSeen_StaysOnSite(t *testing.T) {
	b := newBrowser(t, newFakeAccounts())
	b.post("/signup", url.Values{"invite": {"good-invite"}, "email": {"a@college.edu"}, "password": {"correct horse battery"}})
	for _, back := range []string{"https://evil.example", "//evil.example", "/\\evil.example"} {
		req, _ := http.NewRequest("POST", b.srv.URL+"/seen", strings.NewReader(url.Values{"back": {back}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		res, err := b.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if loc := res.Header.Get("Location"); loc != "/" {
			t.Errorf("back=%q redirected to %q, want /", back, loc)
		}
	}
}

func TestOpenSignup(t *testing.T) {
	b := newBrowser(t, newFakeAccounts())
	if _, page := b.get("/signup"); !strings.Contains(page, "Create account") {
		t.Fatal("anyone should get the sign-up form, no invite needed")
	}
	if status, _ := b.post("/signup", url.Values{"email": {"open@college.edu"}, "password": {"correct horse battery"}}); status != http.StatusSeeOther {
		t.Fatalf("open sign-up: status %d, want a redirect home", status)
	}
}

func TestSignup_LimitedPerNetwork(t *testing.T) {
	b := newBrowser(t, newFakeAccounts())
	for i := range 5 {
		b.post("/logout", nil)
		if status, _ := b.post("/signup", url.Values{"email": {fmt.Sprintf("u%d@college.edu", i)}, "password": {"correct horse battery"}}); status != http.StatusSeeOther {
			t.Fatalf("sign-up %d: status %d", i+1, status)
		}
	}
	b.post("/logout", nil)
	status, page := b.post("/signup", url.Values{"email": {"u6@college.edu"}, "password": {"correct horse battery"}})
	if status != http.StatusTooManyRequests || !strings.Contains(page, "Too many new accounts") {
		t.Errorf("6th sign-up in an hour: status %d, want 429", status)
	}
}

func TestClientIP_BehindProxy(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.5:1234"                          // the host's proxy
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9") // visitor-sent fake, then the proxy's real entry

	direct := &Server{}
	if got := direct.clientIP(r); got != "10.0.0.5" {
		t.Errorf("without TrustProxy: %s, want the connection address", got)
	}
	proxied := &Server{trustProxy: true}
	if got := proxied.clientIP(r); got != "203.0.113.9" {
		t.Errorf("with TrustProxy: %s, want the address the proxy added (not the faked one)", got)
	}
}
