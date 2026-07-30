package places

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EOEboh/prospects-cli/internal/httpx"
	"github.com/EOEboh/prospects-cli/internal/quota"
)

// fakeCounter stands in for the store so the ledger can be driven without a
// database.
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

// testSource wires a source against a fake endpoint. No test in this package
// touches the real Places API.
func testSource(t *testing.T, endpoint string, limits quota.Limits) (*Source, *fakeCounter) {
	t.Helper()

	cache, err := httpx.OpenCache(context.Background(), filepath.Join(t.TempDir(), "cache.db"), time.Hour)
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	t.Cleanup(func() { cache.Close() })

	client, err := httpx.New(httpx.Config{
		UserAgent:   "prospect/test (+mailto:me@example.com)",
		Timeout:     5 * time.Second,
		RatePerHost: 0,
		Cache:       cache,
		Logger:      discard(),
		MaxRetries:  1, // no test needs a retry; keeps the suite fast
	})
	if err != nil {
		t.Fatalf("httpx.New: %v", err)
	}

	counter := &fakeCounter{}
	return New(Config{
		Client:   client,
		Ledger:   quota.NewLedger(counter, limits, discard()),
		APIKey:   "test-key",
		Logger:   discard(),
		Endpoint: endpoint,
	}), counter
}

// placeJSON builds one result in the wire shape Places returns.
func placeJSON(id, name, website, city string, reviews int) string {
	return fmt.Sprintf(`{
		"id": %q,
		"displayName": {"text": %q, "languageCode": "en"},
		"formattedAddress": "100 Main St, %s, TX 78701, USA",
		"addressComponents": [
			{"longText": "100", "shortText": "100", "types": ["street_number"]},
			{"longText": %q, "shortText": %q, "types": ["locality", "political"]},
			{"longText": "Texas", "shortText": "TX", "types": ["administrative_area_level_1", "political"]},
			{"longText": "United States", "shortText": "US", "types": ["country", "political"]}
		],
		"websiteUri": %q,
		"rating": 4.6,
		"userRatingCount": %d
	}`, id, name, city, city, city, website, reviews)
}

func TestDiscoverMapsResults(t *testing.T) {
	var gotMask, gotKey, gotMethod string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMask = r.Header.Get("X-Goog-FieldMask")
		gotKey = r.Header.Get("X-Goog-Api-Key")
		gotMethod = r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)

		fmt.Fprintf(w, `{"places": [%s]}`,
			placeJSON("ChIJ1", "Acme Recruiting", "https://acmerecruiting.com/", "Austin", 27))
	}))
	defer srv.Close()

	src, counter := testSource(t, srv.URL, quota.DefaultLimits())
	businesses, err := src.Discover(context.Background(), "recruiting agency", "Austin, TX", 50)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotKey != "test-key" {
		t.Errorf("API key header = %q", gotKey)
	}
	// A field mask must always be sent: without one Places bills the full
	// response.
	if gotMask == "" {
		t.Fatal("no X-Goog-FieldMask sent")
	}
	for _, want := range []string{"places.id", "places.websiteUri", "places.addressComponents"} {
		if !strings.Contains(gotMask, want) {
			t.Errorf("field mask %q is missing %s", gotMask, want)
		}
	}
	if gotBody["textQuery"] != "recruiting agency in Austin, TX" {
		t.Errorf("textQuery = %v", gotBody["textQuery"])
	}

	if len(businesses) != 1 {
		t.Fatalf("%d businesses, want 1", len(businesses))
	}
	b := businesses[0]
	if b.Name != "Acme Recruiting" {
		t.Errorf("name = %q", b.Name)
	}
	if b.Website != "https://acmerecruiting.com/" {
		t.Errorf("website = %q", b.Website)
	}
	if b.City != "Austin" {
		t.Errorf("city = %q, want Austin from addressComponents", b.City)
	}
	if b.Region != "TX" {
		t.Errorf("region = %q, want the short form TX", b.Region)
	}
	if b.Country != "US" {
		t.Errorf("country = %q, want US", b.Country)
	}
	if b.PlaceID != "ChIJ1" {
		t.Errorf("place id = %q", b.PlaceID)
	}
	if b.ReviewCount == nil || *b.ReviewCount != 27 {
		t.Errorf("review count = %v, want 27", b.ReviewCount)
	}
	if counter.billable != 1 {
		t.Errorf("%d billable calls recorded, want 1", counter.billable)
	}
}

// Every discover call is billed at Enterprise, because websiteUri is an
// Enterprise field and the pipeline is useless without it.
func TestDefaultMaskBillsAtEnterprise(t *testing.T) {
	src, _ := testSource(t, "http://unused", quota.DefaultLimits())

	sku, culprit, err := src.SKU()
	if err != nil {
		t.Fatalf("SKU: %v", err)
	}
	if sku != quota.SKUTextSearchEnterpise {
		t.Errorf("SKU = %s, want text_search_enterprise", sku.Name)
	}
	if culprit != "places.websiteUri" {
		t.Errorf("tier set by %q, want places.websiteUri", culprit)
	}
}

func TestDiscoverPaginates(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body searchRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		n := calls.Add(1)

		switch body.PageToken {
		case "":
			fmt.Fprintf(w, `{"places": [%s], "nextPageToken": "tok2"}`,
				placeJSON("ChIJ1", "First", "https://first.com", "Austin", 10))
		case "tok2":
			fmt.Fprintf(w, `{"places": [%s], "nextPageToken": "tok3"}`,
				placeJSON("ChIJ2", "Second", "https://second.com", "Austin", 20))
		default:
			fmt.Fprintf(w, `{"places": [%s]}`,
				placeJSON("ChIJ3", "Third", "https://third.com", "Austin", 30))
		}
		_ = n
	}))
	defer srv.Close()

	src, counter := testSource(t, srv.URL, quota.DefaultLimits())
	businesses, err := src.Discover(context.Background(), "recruiting agency", "Austin, TX", 50)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(businesses) != 3 {
		t.Fatalf("%d businesses, want 3 across three pages", len(businesses))
	}
	if counter.billable != 3 {
		t.Errorf("%d billable calls, want 3", counter.billable)
	}
}

// Paging stops as soon as the limit is met: a paid call for results the caller
// asked not to receive is waste.
func TestDiscoverStopsAtLimit(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprintf(w, `{"places": [%s, %s], "nextPageToken": "more"}`,
			placeJSON("ChIJ1", "First", "https://first.com", "Austin", 10),
			placeJSON("ChIJ2", "Second", "https://second.com", "Austin", 20))
	}))
	defer srv.Close()

	src, _ := testSource(t, srv.URL, quota.DefaultLimits())
	businesses, err := src.Discover(context.Background(), "x", "y", 2)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(businesses) != 2 {
		t.Errorf("%d businesses, want exactly the 2 requested", len(businesses))
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("%d calls, want 1: the limit was met on the first page", n)
	}
}

// Never more than maxPages, however many tokens the API keeps offering.
func TestDiscoverCapsPages(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		fmt.Fprintf(w, `{"places": [%s], "nextPageToken": "endless"}`,
			placeJSON(fmt.Sprintf("ChIJ%d", n), fmt.Sprintf("Biz %d", n),
				fmt.Sprintf("https://b%d.com", n), "Austin", 5))
	}))
	defer srv.Close()

	src, _ := testSource(t, srv.URL, quota.DefaultLimits())
	if _, err := src.Discover(context.Background(), "x", "y", 1000); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if n := calls.Load(); n > maxPages {
		t.Errorf("%d calls, want at most %d", n, maxPages)
	}
}

// A rerun must not re-buy what was already paid for.
func TestCachedPagesAreFreeAndUnbilled(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprintf(w, `{"places": [%s]}`,
			placeJSON("ChIJ1", "Acme", "https://acme.com", "Austin", 12))
	}))
	defer srv.Close()

	src, counter := testSource(t, srv.URL, quota.DefaultLimits())
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := src.Discover(ctx, "recruiting agency", "Austin, TX", 20); err != nil {
			t.Fatalf("Discover %d: %v", i, err)
		}
	}

	if n := calls.Load(); n != 1 {
		t.Errorf("origin hit %d times, want 1", n)
	}
	if counter.billable != 1 {
		t.Errorf("%d billable calls recorded, want 1", counter.billable)
	}
	if counter.cached != 2 {
		t.Errorf("%d cache hits recorded, want 2", counter.cached)
	}
}

// A changed field mask is a different request and must not be served a cached
// response for a cheaper one.
func TestFieldMaskIsPartOfTheCacheKey(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprintf(w, `{"places": [%s]}`, placeJSON("ChIJ1", "Acme", "https://acme.com", "Austin", 12))
	}))
	defer srv.Close()

	cache, err := httpx.OpenCache(context.Background(), filepath.Join(t.TempDir(), "cache.db"), time.Hour)
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	defer cache.Close()

	client, err := httpx.New(httpx.Config{
		UserAgent: "prospect/test (+mailto:me@example.com)", Timeout: 5 * time.Second,
		Cache: cache, Logger: discard(),
	})
	if err != nil {
		t.Fatalf("httpx.New: %v", err)
	}

	ctx := context.Background()
	counter := &fakeCounter{}
	ledger := quota.NewLedger(counter, quota.DefaultLimits(), discard())

	narrow := New(Config{Client: client, Ledger: ledger, APIKey: "k", Logger: discard(),
		Endpoint: srv.URL, Mask: []string{"places.id", "places.displayName"}})
	wide := New(Config{Client: client, Ledger: ledger, APIKey: "k", Logger: discard(),
		Endpoint: srv.URL, Mask: quota.DefaultTextSearchMask()})

	if _, err := narrow.Discover(ctx, "x", "y", 20); err != nil {
		t.Fatalf("narrow: %v", err)
	}
	if _, err := wide.Discover(ctx, "x", "y", 20); err != nil {
		t.Fatalf("wide: %v", err)
	}

	if n := calls.Load(); n != 2 {
		t.Errorf("origin hit %d times, want 2: a wider mask is a different request", n)
	}
}

// The ceiling stops the run before the call, not after.
func TestCeilingStopsBeforeSpending(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprintf(w, `{"places": [%s]}`, placeJSON("ChIJ1", "Acme", "https://acme.com", "Austin", 12))
	}))
	defer srv.Close()

	// The free-tier ceiling with the month's allowance already spent.
	src, counter := testSource(t, srv.URL, quota.DefaultLimits())
	counter.billable = 100_000

	_, err := src.Discover(context.Background(), "x", "y", 20)
	if err == nil {
		t.Fatal("want a refusal at the ceiling")
	}
	if !errors.Is(err, quota.ErrCeilingReached) {
		t.Errorf("error = %v, want ErrCeilingReached", err)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("%d calls made despite the ceiling; the stop must precede the spend", n)
	}
}

// Hitting the ceiling partway through keeps what was already paid for.
func TestCeilingMidRunKeepsResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body searchRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.PageToken == "" {
			fmt.Fprintf(w, `{"places": [%s], "nextPageToken": "tok2"}`,
				placeJSON("ChIJ1", "First", "https://first.com", "Austin", 10))
			return
		}
		fmt.Fprintf(w, `{"places": [%s]}`,
			placeJSON("ChIJ2", "Second", "https://second.com", "Austin", 20))
	}))
	defer srv.Close()

	// Exactly one call is affordable.
	limits := quota.Limits{Overrides: map[string]int{quota.SKUTextSearchEnterpise.Name: 1}}
	src, _ := testSource(t, srv.URL, limits)

	businesses, err := src.Discover(context.Background(), "x", "y", 50)
	if err != nil {
		t.Fatalf("results gathered before the ceiling should be kept, got error: %v", err)
	}
	if len(businesses) != 1 {
		t.Errorf("%d businesses, want the 1 from the affordable page", len(businesses))
	}
}

// A missing key is a configuration state, not a crash.
func TestDisabledWithoutAPIKey(t *testing.T) {
	src, _ := testSource(t, "http://unused", quota.DefaultLimits())
	src.apiKey = ""

	if src.Enabled() {
		t.Error("a source with no API key must report itself disabled")
	}
	if _, err := src.Discover(context.Background(), "x", "y", 10); err == nil {
		t.Error("Discover without a key should refuse rather than call")
	}
}

// API errors have different fixes, so the message must say which.
func TestAPIErrorsAreActionable(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		wantWords []string
	}{
		{
			name:      "bad key",
			status:    http.StatusForbidden,
			body:      `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"API key not valid"}}`,
			wantWords: []string{"API key", "Places API (New) is enabled"},
		},
		{
			name:      "bad request",
			status:    http.StatusBadRequest,
			body:      `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"invalid field mask"}}`,
			wantWords: []string{"field mask"},
		},
		{
			name:      "google-side quota",
			status:    http.StatusTooManyRequests,
			body:      `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"quota exceeded"}}`,
			wantWords: []string{"Cloud Console quota"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()

			src, _ := testSource(t, srv.URL, quota.DefaultLimits())

			_, err := src.Discover(context.Background(), "x", "y", 20)
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

// An error response must not be cached: one transient failure would otherwise
// become a week of them.
func TestErrorsAreNotCached(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"code":400,"message":"transient"}}`)
			return
		}
		fmt.Fprintf(w, `{"places": [%s]}`, placeJSON("ChIJ1", "Acme", "https://acme.com", "Austin", 12))
	}))
	defer srv.Close()

	src, _ := testSource(t, srv.URL, quota.DefaultLimits())
	ctx := context.Background()

	if _, err := src.Discover(ctx, "x", "y", 20); err == nil {
		t.Fatal("first call should fail")
	}
	businesses, err := src.Discover(ctx, "x", "y", 20)
	if err != nil {
		t.Fatalf("the retry should not be served the cached error: %v", err)
	}
	if len(businesses) != 1 {
		t.Errorf("%d businesses, want 1", len(businesses))
	}
}

func TestDiscoverSkipsDuplicateAndNamelessResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body searchRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.PageToken == "" {
			fmt.Fprintf(w, `{"places": [%s, {"id":"ChIJnone","displayName":{"text":""}}], "nextPageToken": "tok2"}`,
				placeJSON("ChIJ1", "Acme", "https://acme.com", "Austin", 12))
			return
		}
		// The same place again on page two.
		fmt.Fprintf(w, `{"places": [%s]}`, placeJSON("ChIJ1", "Acme", "https://acme.com", "Austin", 12))
	}))
	defer srv.Close()

	src, _ := testSource(t, srv.URL, quota.DefaultLimits())
	businesses, err := src.Discover(context.Background(), "x", "y", 50)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(businesses) != 1 {
		t.Errorf("%d businesses, want 1: the nameless and repeated results should be dropped", len(businesses))
	}
}

func TestEstimateCalls(t *testing.T) {
	src, _ := testSource(t, "http://unused", quota.DefaultLimits())

	tests := []struct{ limit, want int }{
		{1, 1}, {20, 1}, {21, 2}, {40, 2}, {41, 3}, {50, 3},
		{1000, maxPages}, // capped
	}
	for _, tc := range tests {
		if got := src.EstimateCalls(tc.limit); got != tc.want {
			t.Errorf("EstimateCalls(%d) = %d, want %d", tc.limit, got, tc.want)
		}
	}
}

func TestQuery(t *testing.T) {
	tests := []struct{ niche, location, want string }{
		{"recruiting agency", "Austin, TX", "recruiting agency in Austin, TX"},
		{"  dentist  ", "  Leeds  ", "dentist in Leeds"},
		{"plumber", "", "plumber"},
	}
	for _, tc := range tests {
		if got := Query(tc.niche, tc.location); got != tc.want {
			t.Errorf("Query(%q, %q) = %q, want %q", tc.niche, tc.location, got, tc.want)
		}
	}
}

func TestToBusinessFallsBackForCity(t *testing.T) {
	// Some results carry a sublocality but no locality; without a city the
	// name key loses its scope.
	var p place
	if err := json.Unmarshal([]byte(`{
		"id": "x",
		"displayName": {"text": "Corner Shop"},
		"addressComponents": [
			{"longText": "Shoreditch", "shortText": "Shoreditch", "types": ["sublocality", "political"]},
			{"longText": "United Kingdom", "shortText": "GB", "types": ["country", "political"]}
		]
	}`), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	b := p.toBusiness()
	if b.City != "Shoreditch" {
		t.Errorf("city = %q, want the sublocality fallback", b.City)
	}
	if b.Country != "GB" {
		t.Errorf("country = %q, want GB", b.Country)
	}
}

// Places is a discovery source; re-fetching a business already on file would
// spend a billable call for nothing.
func TestCollectIsANoOp(t *testing.T) {
	src, counter := testSource(t, "http://unused", quota.DefaultLimits())
	signals, err := src.Collect(context.Background(), nil)
	if err != nil || signals != nil {
		t.Errorf("Collect = (%v, %v), want (nil, nil)", signals, err)
	}
	if counter.billable != 0 {
		t.Error("Collect must not spend anything")
	}
}
