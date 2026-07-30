package website

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EOEboh/prospects-cli/internal/httpx"
	"github.com/EOEboh/prospects-cli/internal/model"
)

func testSource(t *testing.T) *Source {
	t.Helper()
	cache, err := httpx.OpenCache(context.Background(), filepath.Join(t.TempDir(), "cache.db"), time.Hour)
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	t.Cleanup(func() { cache.Close() })

	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	client, err := httpx.New(httpx.Config{
		UserAgent:   "prospect/test (+mailto:me@example.com)",
		Timeout:     5 * time.Second,
		RatePerHost: 0,
		Cache:       cache,
		Logger:      log,
	})
	if err != nil {
		t.Fatalf("httpx.New: %v", err)
	}
	return New(client, log)
}

func signalMap(signals []model.Signal) map[model.SignalType][]string {
	out := make(map[model.SignalType][]string)
	for _, s := range signals {
		out[s.Type] = append(out[s.Type], s.Value)
	}
	return out
}

// The homepage links to a contact page, and signals found there count for the
// business.
func TestCollectFollowsContactPage(t *testing.T) {
	var paths sync.Map
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths.Store(r.URL.Path, true)
		switch r.URL.Path {
		case "/robots.txt":
			http.NotFound(w, r)
		case "/":
			fmt.Fprint(w, `<html><body><h1>Cedar Staffing</h1>
				<a href="/contact">Contact us</a></body></html>`)
		case "/contact":
			fmt.Fprint(w, `<html><body>
				<p>We respond within 24 hours.</p>
				<form action="/send"><textarea name="msg"></textarea></form>
				<a href="mailto:hello@cedar.com">hello@cedar.com</a>
				</body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	src := testSource(t)
	signals, err := src.Collect(context.Background(), &model.Business{ID: 1, Website: srv.URL})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	got := signalMap(signals)
	if len(got[model.TypeContactForm]) == 0 || got[model.TypeContactForm][0] != "true" {
		t.Errorf("contact_form = %v, want true from the contact page", got[model.TypeContactForm])
	}
	if len(got[model.TypeContactEmail]) == 0 || got[model.TypeContactEmail][0] != "hello@cedar.com" {
		t.Errorf("contact_email = %v", got[model.TypeContactEmail])
	}
	if len(got[model.TypeResponseTimeHours]) == 0 || got[model.TypeResponseTimeHours][0] != "24" {
		t.Errorf("response_time_hours = %v, want 24", got[model.TypeResponseTimeHours])
	}
	if _, visited := paths.Load("/contact"); !visited {
		t.Error("the linked contact page was never fetched")
	}
}

// With no contact link on the homepage, a small number of conventional paths
// are tried.
func TestCollectFallsBackToConventionalPaths(t *testing.T) {
	var tried atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			http.NotFound(w, r)
		case "/":
			fmt.Fprint(w, `<html><body><h1>Cedar</h1><a href="/blog">Blog</a></body></html>`)
		case "/contact":
			tried.Store(true)
			fmt.Fprint(w, `<html><body><form action="/s"><textarea name="m"></textarea></form></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	src := testSource(t)
	signals, err := src.Collect(context.Background(), &model.Business{ID: 1, Website: srv.URL})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if !tried.Load() {
		t.Error("/contact was not tried as a fallback")
	}
	if got := signalMap(signals); got[model.TypeContactForm][0] != "true" {
		t.Errorf("contact_form = %v, want true", got[model.TypeContactForm])
	}
}

// A blocked site records why it was skipped, so "unreadable" stays
// distinguishable from "nothing there".
func TestCollectRecordsRobotsDisallowed(t *testing.T) {
	var fetched atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			fmt.Fprint(w, "User-agent: *\nDisallow: /")
			return
		}
		fetched.Store(true)
		fmt.Fprint(w, "<html><body>secret</body></html>")
	}))
	defer srv.Close()

	src := testSource(t)
	signals, err := src.Collect(context.Background(), &model.Business{ID: 1, Website: srv.URL})
	if err != nil {
		t.Fatalf("a disallowed site is a normal outcome, not an error: %v", err)
	}
	if fetched.Load() {
		t.Error("the page was fetched despite robots.txt")
	}
	if len(signals) != 1 || signals[0].Type != model.TypeRobotsDisallowed {
		t.Fatalf("signals = %v, want a single robots_disallowed", signals)
	}
	if signals[0].Detail == "" {
		t.Error("the disallow signal must record which rule blocked it")
	}
}

// A business with no website is a fact, not a failure.
func TestCollectWithoutWebsite(t *testing.T) {
	src := testSource(t)
	signals, err := src.Collect(context.Background(), &model.Business{ID: 1, Name: "No Site"})
	if err != nil {
		t.Errorf("Collect: %v", err)
	}
	if len(signals) != 0 {
		t.Errorf("signals = %v, want none", signals)
	}
}

// At most three pages per business, however many contact links a site offers.
func TestCollectCapsPagesFetched(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		fmt.Fprint(w, `<html><body>
			<a href="/contact">Contact</a>
			<a href="/contact-us">Get in touch</a>
			<a href="/enquiry">Enquiries</a>
			<a href="/quote">Request a quote</a>
			<a href="/book">Book a call</a>
		</body></html>`)
	}))
	defer srv.Close()

	src := testSource(t)
	if _, err := src.Collect(context.Background(), &model.Business{ID: 1, Website: srv.URL}); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if n := hits.Load(); n > 1+maxContactPages {
		t.Errorf("fetched %d pages, want at most %d", n, 1+maxContactPages)
	}
}

// Off-site "contact" links belong to someone else.
func TestCollectStaysOnTheBusinessHost(t *testing.T) {
	var offsite atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offsite.Store(true)
		fmt.Fprint(w, "<html><body>elsewhere</body></html>")
	}))
	defer other.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `<html><body><a href="%s/contact">Contact us</a></body></html>`, other.URL)
	}))
	defer srv.Close()

	src := testSource(t)
	if _, err := src.Collect(context.Background(), &model.Business{ID: 1, Website: srv.URL}); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if offsite.Load() {
		t.Error("an off-site contact link was followed")
	}
}

// Forms are detected, never submitted.
func TestCollectNeverPostsAnything(t *testing.T) {
	var methods sync.Map
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods.Store(r.Method, true)
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `<html><body>
			<form action="/submit" method="post"><textarea name="m"></textarea></form>
		</body></html>`)
	}))
	defer srv.Close()

	src := testSource(t)
	if _, err := src.Collect(context.Background(), &model.Business{ID: 1, Website: srv.URL}); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	methods.Range(func(k, _ any) bool {
		if k.(string) != http.MethodGet {
			t.Errorf("issued a %s request; this tool only ever reads", k)
		}
		return true
	})
}

func TestNormalizeStartURL(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"acme.com", "https://acme.com/", false},
		{"https://acme.com", "https://acme.com/", false},
		{"http://acme.com/contact", "http://acme.com/contact", false},
		{"  acme.com  ", "https://acme.com/", false},
		{"ftp://acme.com", "", true},
		{"", "", true},
	}
	for _, tc := range tests {
		got, err := normalizeStartURL(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("normalizeStartURL(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizeStartURL(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("normalizeStartURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Reading only the homepage is weaker evidence than confirming on a contact
// page, and the confidence recorded says so.
func TestConfidenceReflectsPagesRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			http.NotFound(w, r)
		case "/":
			fmt.Fprint(w, `<html><body><h1>Solo</h1></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	src := testSource(t)
	signals, err := src.Collect(context.Background(), &model.Business{ID: 1, Website: srv.URL})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(signals) == 0 {
		t.Fatal("no signals recorded")
	}
	for _, s := range signals {
		if s.Confidence >= 0.9 {
			t.Errorf("confidence %v for a homepage-only read, want lower", s.Confidence)
		}
	}
}
