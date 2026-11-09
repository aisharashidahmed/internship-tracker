package web

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"internship-tracker/internal/auth"
	"internship-tracker/internal/store"
)

// Accounts is what the server needs for sign-up, login and sessions.
// *store.Store satisfies it; tests use an in-memory fake.
type Accounts interface {
	InviteUsable(ctx context.Context, token string) (bool, error)
	SignUp(ctx context.Context, inviteToken, email, passwordHash string) (store.User, error)
	UserByEmail(ctx context.Context, email string) (store.User, bool, error)
	CreateSession(ctx context.Context, userID int64) (string, time.Time, error)
	SessionUser(ctx context.Context, token string) (u store.User, found, renewed bool, expires time.Time, err error)
	DeleteSession(ctx context.Context, token string) error
	ViewFeed(ctx context.Context, userID int64) (time.Time, error)
	FeedNewSince(ctx context.Context, userID int64) (time.Time, bool, error)
	MarkAllSeen(ctx context.Context, userID int64) error
}

const sessionCookie = "session"

// Failed logins allowed per 15 minutes: per email (stops guessing one
// person's password) and per IP address (stops trying many emails).
const loginWindow = 15 * time.Minute

type userKey struct{}

// currentUser is the logged-in user for this request, or nil.
func currentUser(r *http.Request) *store.User {
	u, _ := r.Context().Value(userKey{}).(*store.User)
	return u
}

// withUser reads the session cookie on every request and, if it's valid,
// puts the user in the request's context. A renewed session gets a fresh
// cookie expiry so the browser keeps it as long as the server does.
func (s *Server) withUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err == nil && c.Value != "" {
			u, found, renewed, expires, err := s.accounts.SessionUser(r.Context(), c.Value)
			switch {
			case err != nil:
				log.Printf("session lookup: %v", err) // treat as logged out; don't break the page
			case found:
				if renewed {
					s.setSessionCookie(w, r, c.Value, expires)
				}
				r = r.WithContext(context.WithValue(r.Context(), userKey{}, &u))
			default:
				s.clearSessionCookie(w, r) // expired or unknown: tidy up the browser
			}
		}
		next.ServeHTTP(w, r)
	})
}

// setSessionCookie: HttpOnly so page scripts can't read it; SameSite=Lax so
// other sites can't make your browser send it on their form posts; Secure
// whenever the site is served over HTTPS.
func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", Expires: expires,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.secureCookies || r.TLS != nil,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.secureCookies || r.TLS != nil,
	})
}

// AuthPage is the data for the login and sign-up pages.
type AuthPage struct {
	User     *store.User
	Title    string
	Mode     string // "login" or "signup"
	Email    string // re-filled after an error, so it needn't be retyped
	Error    string
	Invite   string
	InviteOK bool
}

func (s *Server) render(w http.ResponseWriter, status int, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render %s: %v", name, err)
	}
}

// --- Sign-up ---

func (s *Server) signupPage(w http.ResponseWriter, r *http.Request) {
	invite := r.URL.Query().Get("invite")
	ok, err := s.accounts.InviteUsable(r.Context(), invite)
	if err != nil {
		serverError(w, err)
		return
	}
	s.render(w, http.StatusOK, "auth.html", AuthPage{
		User: currentUser(r), Title: "Create your account", Mode: "signup", Invite: invite, InviteOK: ok,
	})
}

func (s *Server) signup(w http.ResponseWriter, r *http.Request) {
	page := AuthPage{Title: "Create your account", Mode: "signup", InviteOK: true,
		Invite: r.PostFormValue("invite"), Email: strings.TrimSpace(r.PostFormValue("email"))}
	fail := func(msg string) {
		page.Error = msg
		s.render(w, http.StatusUnprocessableEntity, "auth.html", page)
	}

	email, err := auth.NormalizeEmail(page.Email)
	if err != nil {
		fail(err.Error())
		return
	}
	password := r.PostFormValue("password")
	if err := auth.CheckPassword(password); err != nil {
		fail(err.Error())
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		serverError(w, err)
		return
	}

	u, err := s.accounts.SignUp(r.Context(), page.Invite, email, hash)
	switch {
	case errors.Is(err, store.ErrInviteInvalid):
		page.InviteOK = false
		s.render(w, http.StatusUnprocessableEntity, "auth.html", page)
		return
	case errors.Is(err, store.ErrEmailTaken):
		// Neutral on purpose: don't confirm to a stranger that this email
		// has an account. The real owner knows to log in instead.
		fail("We couldn't create an account with that email. If you already have one, log in instead.")
		return
	case err != nil:
		serverError(w, err)
		return
	}
	s.startSession(w, r, u.ID)
}

// --- Login and logout ---

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if currentUser(r) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, http.StatusOK, "auth.html", AuthPage{Title: "Log in", Mode: "login"})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	page := AuthPage{Title: "Log in", Mode: "login", Email: strings.TrimSpace(r.PostFormValue("email"))}
	emailKey, ipKey := "email:"+strings.ToLower(page.Email), "ip:"+clientIP(r)

	if s.emailLimiter.Blocked(emailKey) || s.ipLimiter.Blocked(ipKey) {
		page.Error = "Too many attempts. Please wait 15 minutes and try again."
		s.render(w, http.StatusTooManyRequests, "auth.html", page)
		return
	}

	u, found, err := s.accounts.UserByEmail(r.Context(), page.Email)
	if err != nil {
		serverError(w, err)
		return
	}
	// With no account, PasswordMatches still does the slow check against a
	// dummy hash, so the response time doesn't reveal which emails exist.
	if !auth.PasswordMatches(u.PasswordHash, r.PostFormValue("password")) || !found {
		s.emailLimiter.Fail(emailKey)
		s.ipLimiter.Fail(ipKey)
		page.Error = "Email or password is incorrect."
		s.render(w, http.StatusUnauthorized, "auth.html", page)
		return
	}
	s.emailLimiter.Reset(emailKey)
	s.startSession(w, r, u.ID)
}

// startSession logs the user in with a brand-new token (never one the
// browser already had, so a planted cookie can't be reused) and goes home.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, userID int64) {
	token, expires, err := s.accounts.CreateSession(r.Context(), userID)
	if err != nil {
		serverError(w, err)
		return
	}
	s.setSessionCookie(w, r, token, expires)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := s.accounts.DeleteSession(r.Context(), c.Value); err != nil {
			log.Printf("logout: %v", err)
		}
	}
	s.clearSessionCookie(w, r)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// clientIP is the address the request came from. Behind a proxy this would
// be the proxy; we'll read its forwarding header once we deploy behind one.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
