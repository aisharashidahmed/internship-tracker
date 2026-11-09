package safefetch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestCheck_BlocksSSRFTargets(t *testing.T) {
	for _, u := range []string{
		"http://localhost/admin",
		"http://LOCALHOST./",
		"http://127.0.0.1/",
		"http://127.1/",                            // short form of 127.0.0.1 (rejected as invalid or private)
		"http://169.254.169.254/latest/meta-data/", // cloud metadata service
		"http://10.0.0.5/",
		"http://192.168.1.1/",
		"http://172.16.3.4/",
		"http://[::1]/",
		"http://[::ffff:127.0.0.1]/", // IPv4 loopback dressed as IPv6
		"http://100.64.0.1/",         // carrier-grade NAT
		"http://metadata.google.internal/",
		"file:///etc/passwd",
		"gopher://example.com/",
		"ftp://example.com/",
		"https://example.com:8080/",      // non-web port
		"https://user:pass@example.com/", // credentials in the link
	} {
		if _, err := Check(u); err == nil {
			t.Errorf("Check(%q) allowed it", u)
		}
	}
	for _, u := range []string{"https://job-boards.greenhouse.io/x/jobs/123", "http://example.com/", "https://example.com:443/a"} {
		if _, err := Check(u); err != nil {
			t.Errorf("Check(%q) = %v, want allowed", u, err)
		}
	}
}

func TestPublic(t *testing.T) {
	for ip, want := range map[string]bool{
		"8.8.8.8": true, "140.82.112.3": true, "2606:4700::1111": true,
		"127.0.0.1": false, "10.1.2.3": false, "169.254.169.254": false, "::1": false, "fd00::1": false, "0.0.0.0": false,
	} {
		if got := Public(netip.MustParseAddr(ip)); got != want {
			t.Errorf("Public(%s) = %v, want %v", ip, got, want)
		}
	}
}

// A public-looking name that resolves to a private address is refused at
// connect time (the check happens on the resolved IP, not the name).
func TestGet_NameResolvingToPrivateIsBlocked(t *testing.T) {
	c := &Client{lookupFn: fakeLookup("10.0.0.7")}
	_, err := c.Get(context.Background(), "http://innocent-looking.example/")
	if !errors.Is(err, ErrBlocked) {
		t.Errorf("err = %v, want ErrBlocked", err)
	}
}

// A redirect to a private address (or another site) is refused too.
func TestGet_RedirectToPrivateIsBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer srv.Close()
	c := &Client{forTests: true}
	if _, err := c.Get(context.Background(), srv.URL); err == nil {
		t.Error("followed a redirect to the metadata address")
	}
}

func TestGet_SizeLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("x", MaxBytes+10)))
	}))
	defer srv.Close()
	c := &Client{forTests: true}
	if _, err := c.Get(context.Background(), srv.URL); !errors.Is(err, ErrTooBig) {
		t.Errorf("err = %v, want ErrTooBig", err)
	}
}

func TestGet_LoopbackBlockedByDefault(t *testing.T) {
	// A name that resolves to loopback, on the standard port.
	c := &Client{lookupFn: fakeLookup("127.0.0.1")}
	if _, err := c.Get(context.Background(), "http://looks-public.example/"); !errors.Is(err, ErrBlocked) {
		t.Errorf("err = %v, want ErrBlocked", err)
	}
}

// fakeLookup answers every DNS lookup with ip.
func fakeLookup(ip string) func(context.Context, string) ([]netip.Addr, error) {
	return func(context.Context, string) ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr(ip)}, nil }
}

func TestGet_FetchesAPublicPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hello")) }))
	defer srv.Close()
	res, err := (&Client{forTests: true}).Get(context.Background(), srv.URL)
	if err != nil || string(res.Body) != "hello" {
		t.Errorf("Get = %q, %v", res.Body, err)
	}
}
