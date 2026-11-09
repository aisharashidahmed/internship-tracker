package web

import (
	"context"
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
	invites  map[string]int // token -> seats left
	users    map[string]store.User
	sessions map[string]int64 // token -> user id
	nextID   int64
}

func newFakeAccounts() *fakeAccounts {
	return &fakeAccounts{invites: map[string]int{"good-invite": 1}, users: map[string]store.User{}, sessions: map[string]int64{}}
}

func (f *fakeAccounts) InviteUsable(_ context.Context, t string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.invites[t] > 0, nil
}

func (f *fakeAccounts) SignUp(_ context.Context, invite, email, hash string) (store.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.invites[invite] <= 0 {
		return store.User{}, store.ErrInviteInvalid
	}
	if _, taken := f.users[strings.ToLower(email)]; taken {
		return store.User{}, store.ErrEmailTaken
	}
	f.invites[invite]--
	f.nextID++
	u := store.User{ID: f.nextID, Email: email, PasswordHash: hash}
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
			return store.User{ID: u.ID, Email: u.Email}, true, false, time.Now().Add(time.Hour), nil
		}
	}
	return store.User{}, false, false, time.Time{}, nil
}

func (f *fakeAccounts) DeleteSession(_ context.Context, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, token)
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
	t.Helper()
	s, err := New(fakeFeed{}, accounts, Options{})
	if err != nil {
		t.Fatal(err)
	}
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
	if page := signup("made-up", "a@college.edu", "correct horse battery"); !strings.Contains(page, "invite-only") {
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
