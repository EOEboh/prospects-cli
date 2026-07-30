// Package places discovers businesses through the Google Places API (New).
//
// This is the only source in the tool that can cost money, so the spending
// controls are not incidental to it. Every request sends an explicit field
// mask, every response is cached, and every live call is checked against a
// monthly ceiling before it is made — see internal/quota.
//
// Without GOOGLE_PLACES_API_KEY the source reports itself disabled and the
// pipeline runs without it. Discovery is an upgrade over hand-built CSVs, not
// a dependency.
package places

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/EOEboh/prospects-cli/internal/httpx"
	"github.com/EOEboh/prospects-cli/internal/model"
	"github.com/EOEboh/prospects-cli/internal/quota"
)

// searchTextEndpoint is Places API (New). The legacy Places API takes a
// `fields` query parameter instead of a field mask header and is on Google's
// deprecation path, so it is not used here.
const searchTextEndpoint = "https://places.googleapis.com/v1/places:searchText"

// maxPageSize is the largest page Text Search will return. Fewer, larger pages
// means fewer billable calls for the same number of results.
const maxPageSize = 20

// maxPages bounds a single discovery run. Text Search stops yielding new
// results after a few pages, and an unbounded loop against a paid endpoint is
// how a ceiling gets reached by accident.
const maxPages = 3

// Source implements source.Discoverer over Google Places.
type Source struct {
	client   *httpx.Client
	ledger   *quota.Ledger
	apiKey   string
	mask     []string
	endpoint string
	log      *slog.Logger
	runID    *int64
}

// Config configures a Places source.
type Config struct {
	Client *httpx.Client
	Ledger *quota.Ledger
	APIKey string
	Logger *slog.Logger
	RunID  *int64

	// Mask overrides the default field mask. Anything added here can change
	// the billing tier, which is why quota.TierForFields resolves it rather
	// than the caller asserting a cost.
	Mask []string

	// Endpoint overrides the Text Search URL. Tests point this at a local
	// server; nothing else should set it.
	Endpoint string
}

func New(cfg Config) *Source {
	mask := cfg.Mask
	if len(mask) == 0 {
		mask = quota.DefaultTextSearchMask()
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = searchTextEndpoint
	}
	return &Source{
		client:   cfg.Client,
		ledger:   cfg.Ledger,
		apiKey:   strings.TrimSpace(cfg.APIKey),
		mask:     mask,
		endpoint: endpoint,
		log:      cfg.Logger,
		runID:    cfg.RunID,
	}
}

func (s *Source) Name() model.SignalSource { return model.SourcePlaces }

// Enabled reports whether discovery can run. A missing key is not an error:
// the source stands down and the rest of the pipeline continues.
func (s *Source) Enabled() bool {
	return s.apiKey != "" && s.client != nil && s.ledger != nil
}

// Collect satisfies source.Source. Places is a discovery source rather than an
// enrichment one: it finds businesses that are not in the database yet, and has
// nothing to add about one already there. Spending a billable call to re-fetch
// what is already stored would be the opposite of the point.
func (s *Source) Collect(context.Context, *model.Business) ([]model.Signal, error) {
	return nil, nil
}

// SKU resolves what a call with the configured mask will be billed at, along
// with the field responsible for the tier.
func (s *Source) SKU() (quota.SKU, string, error) {
	return quota.TextSearchSKU(s.mask)
}

// EstimateCalls reports the worst-case number of billable calls Discover would
// make.
//
// Worst case, not exact: a paged API only reveals how many pages exist by
// being called, and cached pages cost nothing. A real run is usually cheaper.
func (s *Source) EstimateCalls(limit int) int {
	pages := (limit + maxPageSize - 1) / maxPageSize
	if pages < 1 {
		pages = 1
	}
	if pages > maxPages {
		pages = maxPages
	}
	return pages
}

// Query builds the Text Search query string from a niche and a location.
func Query(niche, location string) string {
	niche = strings.TrimSpace(niche)
	location = strings.TrimSpace(location)
	if location == "" {
		return niche
	}
	return niche + " in " + location
}

// Discover searches for businesses matching a niche and location.
//
// Limit is a ceiling, not a target: fewer real results means fewer rows, and
// the run stops early rather than paging for the sake of it.
func (s *Source) Discover(ctx context.Context, niche, location string, limit int) ([]model.Business, error) {
	if !s.Enabled() {
		return nil, errors.New("google places is not configured: set GOOGLE_PLACES_API_KEY")
	}
	if limit <= 0 {
		return nil, nil
	}

	sku, culprit, err := s.SKU()
	if err != nil {
		return nil, err
	}
	s.log.Debug("places field mask resolved",
		"sku", sku.Name, "tier", sku.Tier.Name, "tier_set_by", culprit)

	query := Query(niche, location)

	var (
		out       []model.Business
		pageToken string
		seen      = make(map[string]bool)
	)

	for page := 1; page <= maxPages && len(out) < limit; page++ {
		resp, err := s.searchPage(ctx, query, pageToken, sku, page)
		if err != nil {
			// A ceiling reached partway through is not a failure: the results
			// already gathered are real and worth keeping.
			if errors.Is(err, quota.ErrCeilingReached) && len(out) > 0 {
				s.log.Warn("stopping discovery at the monthly ceiling",
					"gathered", len(out), "error", err)
				return out, nil
			}
			return out, err
		}

		for _, p := range resp.Places {
			if len(out) >= limit {
				break
			}
			// The same place can appear on two pages; billing does not care,
			// but the caller should not see it twice.
			if p.ID != "" && seen[p.ID] {
				continue
			}
			seen[p.ID] = true

			b := p.toBusiness()
			if b.Name == "" {
				continue
			}
			out = append(out, b)
		}

		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}

	return out, nil
}

// searchPage performs one billable Text Search call, or serves it from cache.
func (s *Source) searchPage(ctx context.Context, query, pageToken string, sku quota.SKU, page int) (*searchResponse, error) {
	body, err := json.Marshal(searchRequest{
		TextQuery: query,
		PageSize:  maxPageSize,
		PageToken: pageToken,
	})
	if err != nil {
		return nil, err
	}

	maskHeader := quota.FieldMaskHeader(s.mask)

	resp, err := s.client.API(ctx, httpx.APIRequest{
		Method: http.MethodPost,
		URL:    s.endpoint,
		Body:   body,
		Headers: map[string]string{
			"X-Goog-Api-Key":   s.apiKey,
			"X-Goog-FieldMask": maskHeader,
		},
		// The body and the mask both change the response, so both belong in
		// the cache key. The API key does not: rotating it must not orphan a
		// week of cached results.
		CacheKeyExtra: []string{string(body), maskHeader},

		// The ceiling is checked here rather than before the cache lookup, so
		// a cache hit is neither counted nor refused.
		BeforeNetwork: func() error {
			return s.ledger.Check(ctx, sku, 1)
		},
		AfterNetwork: func() {
			if err := s.ledger.Record(ctx, sku, "places:searchText", false, s.runID); err != nil {
				s.log.Error("could not record billable call", "sku", sku.Name, "error", err)
			}
		},
	})
	if err != nil {
		// A status that survived every retry still carries Google's
		// explanation; surface that rather than "returned 429".
		var statusErr *httpx.StatusError
		if errors.As(err, &statusErr) {
			return nil, apiError(&httpx.Response{Status: statusErr.Status, Body: statusErr.Body})
		}
		return nil, err
	}

	if resp.FromCache {
		s.log.Debug("places page served from cache", "page", page, "query", query)
		if err := s.ledger.Record(ctx, sku, "places:searchText", true, s.runID); err != nil {
			s.log.Debug("could not record cache hit", "error", err)
		}
	}

	if resp.Status != http.StatusOK {
		return nil, apiError(resp)
	}

	var parsed searchResponse
	if err := json.Unmarshal(resp.Body, &parsed); err != nil {
		return nil, fmt.Errorf("decode places response: %w", err)
	}
	return &parsed, nil
}

// apiError turns a non-200 into something actionable. A wrong key and an
// exhausted quota are different problems with different fixes, and the message
// should say which.
func apiError(resp *httpx.Response) error {
	var payload struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	_ = json.Unmarshal(resp.Body, &payload)

	msg := strings.TrimSpace(payload.Error.Message)
	if msg == "" {
		msg = strings.TrimSpace(string(resp.Body))
	}

	switch resp.Status {
	case http.StatusBadRequest:
		return fmt.Errorf("places rejected the request (%s). Check the field mask and query: %s",
			payload.Error.Status, msg)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("places refused the API key (%d %s): %s\n"+
			"Check that the key is valid, that the Places API (New) is enabled for the project, "+
			"and that any key restrictions allow it", resp.Status, payload.Error.Status, msg)
	case http.StatusTooManyRequests:
		return fmt.Errorf("places quota exhausted on Google's side (429): %s\n"+
			"This is a Cloud Console quota, separate from PROSPECT_PLACES_MONTHLY_MAX", msg)
	default:
		return fmt.Errorf("places returned %d: %s", resp.Status, msg)
	}
}
