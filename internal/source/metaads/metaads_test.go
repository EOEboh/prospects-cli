package metaads

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EOEboh/prospects-cli/internal/httpx"
	"github.com/EOEboh/prospects-cli/internal/model"
	"github.com/EOEboh/prospects-cli/internal/quota"
)

type fakeCounter struct {
	mu       sync.Mutex
	billable int
	cached   int
}

func (f *fakeCounter) BillableCalls(context.Context, quota.SKU, string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.billable, nil
}

func (f *fakeCounter) RecordCall(_ context.Context, _ quota.SKU, _ string, billable, cached bool, _ *int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if billable {
		f.billable++
	}
	if cached {
		f.cached++
	}
	return nil
}

func discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// testSource wires a source against a fake endpoint. No test here touches the
// real Ad Library API.
func testSource(t *testing.T, endpoint string) (*Source, *fakeCounter) {
	t.Helper()

	cache, err := httpx.OpenCache(context.Background(), filepath.Join(t.TempDir(), "cache.db"), time.Hour)
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	t.Cleanup(func() { cache.Close() })

	client, err := httpx.New(httpx.Config{
		UserAgent:  "prospect/test (+mailto:me@example.com)",
		Timeout:    5 * time.Second,
		Cache:      cache,
		Logger:     discard(),
		MaxRetries: 1,
	})
	if err != nil {
		t.Fatalf("httpx.New: %v", err)
	}

	counter := &fakeCounter{}
	return New(Config{
		Client:   client,
		Ledger:   quota.NewLedger(counter, quota.DefaultLimits(), discard()),
		Token:    "test-token",
		Logger:   discard(),
		Endpoint: endpoint,
	}), counter
}

func adsJSON(pageNames ...string) string {
	entries := make([]string, 0, len(pageNames))
	for i, name := range pageNames {
		entries = append(entries, fmt.Sprintf(
			`{"id":"ad%d","page_name":%q,"ad_delivery_start_time":"2026-07-01","ad_snapshot_url":"https://example.com/ad%d"}`,
			i, name, i))
	}
	return `{"data":[` + strings.Join(entries, ",") + `]}`
}

func firstSignal(t *testing.T, signals []model.Signal) model.Signal {
	t.Helper()
	if len(signals) != 1 {
		t.Fatalf("%d signals, want 1", len(signals))
	}
	return signals[0]
}

func TestCollectFindsActiveAds(t *testing.T) {
	var (
		got       url.Values
		gotAuth   string
		gotRawURL string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		gotAuth = r.Header.Get("Authorization")
		gotRawURL = r.URL.String()
		fmt.Fprint(w, adsJSON("Acme Recruiting"))
	}))
	defer srv.Close()

	src, counter := testSource(t, srv.URL)
	signals, err := src.Collect(context.Background(), &model.Business{
		ID: 1, Name: "Acme Recruiting", Country: "US",
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// Only currently-running ads answer the question being asked.
	if got.Get("ad_active_status") != "ACTIVE" {
		t.Errorf("ad_active_status = %q, want ACTIVE", got.Get("ad_active_status"))
	}
	if got.Get("search_terms") != "Acme Recruiting" {
		t.Errorf("search_terms = %q", got.Get("search_terms"))
	}
	if got.Get("ad_reached_countries") != `["US"]` {
		t.Errorf("ad_reached_countries = %q, want [\"US\"]", got.Get("ad_reached_countries"))
	}
	// The token travels as a header, so it never lands in a URL, a log or a
	// cache key.
	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q, want the bearer token", gotAuth)
	}
	if strings.Contains(gotRawURL, "test-token") {
		t.Errorf("the token leaked into the URL: %s", gotRawURL)
	}

	s := firstSignal(t, signals)
	if s.Type != model.TypeRunningAds || s.Value != "true" {
		t.Errorf("signal = %s=%s, want running_ads=true", s.Type, s.Value)
	}
	if s.Source != model.SourceMetaAds {
		t.Errorf("source = %q, want meta_ads", s.Source)
	}
	// A name-matched API hit is weaker evidence than a person looking.
	if s.Confidence >= 1.0 {
		t.Errorf("confidence = %v, want below 1.0 for a name-matched result", s.Confidence)
	}
	if counter.billable != 1 {
		t.Errorf("%d calls recorded, want 1", counter.billable)
	}
}

// "We looked and found nothing" has to stay distinguishable from "we never
// looked", so a negative result is recorded too.
func TestCollectRecordsAbsence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer srv.Close()

	src, _ := testSource(t, srv.URL)
	signals, err := src.Collect(context.Background(), &model.Business{ID: 1, Name: "Acme Recruiting"})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	s := firstSignal(t, signals)
	if s.Value != "false" {
		t.Errorf("value = %q, want false", s.Value)
	}
	// Absence from an incomplete archive is weak evidence, recorded as such.
	if s.Confidence >= 1.0 {
		t.Errorf("confidence = %v, want below 1.0", s.Confidence)
	}
}

// The archive matches on page name, so results belonging to a same-named but
// unrelated advertiser have to be filtered out.
func TestMatchAdsFiltersUnrelatedAdvertisers(t *testing.T) {
	tests := []struct {
		name         string
		businessName string
		pages        []string
		wantMatched  int
	}{
		{"exact", "Acme Recruiting", []string{"Acme Recruiting"}, 1},
		{"legal suffix on the ad", "Acme Recruiting", []string{"Acme Recruiting Ltd"}, 1},
		{"legal suffix on the business", "Acme Recruiting LLC", []string{"Acme Recruiting"}, 1},
		{"case and punctuation", "acme, recruiting", []string{"ACME Recruiting"}, 1},
		{"unrelated advertiser", "Acme Recruiting", []string{"Globex Industries"}, 0},
		{"partial word is not a match", "Acme Recruiting", []string{"Acme Plumbing"}, 0},
		{"several ads from one page", "Acme Recruiting", []string{"Acme Recruiting", "Acme Recruiting"}, 2},
		{"mixed", "Acme Recruiting", []string{"Acme Recruiting", "Globex"}, 1},
		{"empty page name", "Acme Recruiting", []string{""}, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ads := make([]ad, 0, len(tc.pages))
			for _, p := range tc.pages {
				ads = append(ads, ad{PageName: p})
			}
			matched, _ := matchAds(tc.businessName, ads)
			if matched != tc.wantMatched {
				t.Errorf("matchAds(%q, %v) = %d, want %d", tc.businessName, tc.pages, matched, tc.wantMatched)
			}
		})
	}
}

// The matched page names are recorded so a human can judge the match.
func TestCollectRecordsMatchedPagesAndCaveat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, adsJSON("Acme Recruiting Ltd", "Globex Industries"))
	}))
	defer srv.Close()

	src, _ := testSource(t, srv.URL)
	signals, err := src.Collect(context.Background(), &model.Business{ID: 1, Name: "Acme Recruiting"})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	var detail map[string]any
	if err := json.Unmarshal([]byte(firstSignal(t, signals).Detail), &detail); err != nil {
		t.Fatalf("detail is not JSON: %v", err)
	}

	pages, _ := detail["matched_pages"].([]any)
	if len(pages) != 1 || pages[0] != "Acme Recruiting Ltd" {
		t.Errorf("matched_pages = %v, want only the Acme page", detail["matched_pages"])
	}
	// The detail must say how the match was made and that coverage is partial;
	// a bare "true" would overstate what this source knows.
	if detail["match_method"] == nil {
		t.Error("detail should record that matching is by page name")
	}
	if detail["coverage_caveat"] == nil {
		t.Error("detail should record the coverage caveat")
	}
}

// A business's own country beats the configured fallback: searching the wrong
// market returns nothing.
func TestCountryComesFromTheBusinessWhenKnown(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("ad_reached_countries")
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer srv.Close()

	src, _ := testSource(t, srv.URL)
	src.countries = []string{"US"}

	if _, err := src.Collect(context.Background(), &model.Business{
		ID: 1, Name: "Leeds Plumbing", Country: "gb",
	}); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got != `["GB"]` {
		t.Errorf("ad_reached_countries = %q, want [\"GB\"] from the business", got)
	}

	// With no country on the business, the configured fallback applies.
	if _, err := src.Collect(context.Background(), &model.Business{ID: 2, Name: "Somewhere Co"}); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got != `["US"]` {
		t.Errorf("ad_reached_countries = %q, want the [\"US\"] fallback", got)
	}
}

// A rotated token must not orphan every cached answer.
func TestTokenIsNotPartOfTheCacheKey(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, adsJSON("Acme Recruiting"))
	}))
	defer srv.Close()

	src, counter := testSource(t, srv.URL)
	ctx := context.Background()
	b := &model.Business{ID: 1, Name: "Acme Recruiting", Country: "US"}

	if _, err := src.Collect(ctx, b); err != nil {
		t.Fatalf("first Collect: %v", err)
	}
	src.token = "rotated-token"
	if _, err := src.Collect(ctx, b); err != nil {
		t.Fatalf("second Collect: %v", err)
	}

	if n := calls.Load(); n != 1 {
		t.Errorf("origin hit %d times, want 1: a rotated token must still hit the cache", n)
	}
	if counter.cached != 1 {
		t.Errorf("%d cache hits recorded, want 1", counter.cached)
	}
}

func TestDisabledWithoutToken(t *testing.T) {
	src, _ := testSource(t, "http://unused")
	src.token = ""

	if src.Enabled() {
		t.Error("a source with no token must report itself disabled")
	}
	// Disabled is a no-op, not an error: the manual path covers this signal.
	signals, err := src.Collect(context.Background(), &model.Business{ID: 1, Name: "Acme"})
	if err != nil || signals != nil {
		t.Errorf("Collect = (%v, %v), want (nil, nil)", signals, err)
	}
}

// With no name there is nothing to search on.
func TestCollectWithoutName(t *testing.T) {
	src, counter := testSource(t, "http://unused")
	signals, err := src.Collect(context.Background(), &model.Business{ID: 1, Website: "https://acme.com"})
	if err != nil {
		t.Errorf("Collect: %v", err)
	}
	if signals != nil {
		t.Errorf("signals = %v, want none", signals)
	}
	if counter.billable != 0 {
		t.Error("a nameless business must not cost a call")
	}
}

// An expired token and a rate limit have different fixes.
func TestAPIErrorsAreActionable(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		wantWords []string
	}{
		{
			name:      "expired token",
			status:    http.StatusBadRequest,
			body:      `{"error":{"message":"Error validating access token","code":190}}`,
			wantWords: []string{"tokens expire", "prospect signal"},
		},
		{
			name:      "rate limited",
			status:    http.StatusBadRequest,
			body:      `{"error":{"message":"User request limit reached","code":17}}`,
			wantWords: []string{"rate limited", "PROSPECT_WORKERS"},
		},
		{
			name:      "bad request",
			status:    http.StatusBadRequest,
			body:      `{"error":{"message":"ad_reached_countries is required","code":100}}`,
			wantWords: []string{"ad_reached_countries"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()

			src, _ := testSource(t, srv.URL)
			_, err := src.Collect(context.Background(), &model.Business{ID: 1, Name: "Acme"})
			if err == nil {
				t.Fatal("want an error")
			}
			for _, want := range tc.wantWords {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

// The Ad Library is free but throttled, so the cap exists to stay polite.
func TestCapStopsRunawayCalls(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer srv.Close()

	src, counter := testSource(t, srv.URL)
	counter.billable = quota.DefaultUnmeteredCap

	_, err := src.Collect(context.Background(), &model.Business{ID: 1, Name: "Acme"})
	if err == nil {
		t.Fatal("want a refusal at the cap")
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("%d calls made past the cap, want 0", n)
	}
}

func TestQuotedList(t *testing.T) {
	tests := []struct {
		in   []string
		want string
	}{
		{[]string{"US"}, `"US"`},
		{[]string{"us", "gb"}, `"US","GB"`},
		{[]string{" de "}, `"DE"`},
	}
	for _, tc := range tests {
		if got := quotedList(tc.in); got != tc.want {
			t.Errorf("quotedList(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
