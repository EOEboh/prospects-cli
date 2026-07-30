// Package metaads queries the Meta Ad Library API for active ads.
//
// This source is optional and off by default, and the pipeline is fully useful
// without it. The public Ad Library API's coverage of non-EU commercial ads is
// unreliable, and it matches advertisers by page name rather than by website,
// so a result here is a hint rather than a finding.
//
// The dependable path for the ad signal is a human checking the Ad Library web
// UI and recording the answer with `prospect signal`. That is why signals from
// this source carry lower confidence than manual ones and, by default, do not
// clear the "needs an ad check" flag.
package metaads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/EOEboh/prospects-cli/internal/dedup"
	"github.com/EOEboh/prospects-cli/internal/httpx"
	"github.com/EOEboh/prospects-cli/internal/model"
	"github.com/EOEboh/prospects-cli/internal/quota"
)

// adsArchiveEndpoint is the Ad Library search endpoint. Pinned to a version so
// a Graph API rollover cannot silently change the response shape.
const adsArchiveEndpoint = "https://graph.facebook.com/v21.0/ads_archive"

// apiConfidence is how much weight a name-matched API result carries.
//
// Below the 0.7 default confidence floor on purpose: the Ad Library matches on
// page name, so "Summit Search" can return ads belonging to an unrelated
// advertiser of the same name. A human checking the web UI records 1.0.
const apiConfidence = 0.5

// maxResults bounds one lookup. A business with more active ads than this is
// still simply "running ads"; fetching more would tell us nothing new.
const maxResults = 25

// Source implements source.Source over the Meta Ad Library.
type Source struct {
	client    *httpx.Client
	ledger    *quota.Ledger
	token     string
	countries []string
	endpoint  string
	log       *slog.Logger
	runID     *int64
}

// Config configures an Ad Library source.
type Config struct {
	Client *httpx.Client
	Ledger *quota.Ledger
	Token  string
	Logger *slog.Logger
	RunID  *int64

	// Countries is the ad_reached_countries filter, which the API requires.
	// A business's own country is preferred when known; this is the fallback.
	Countries []string

	// Endpoint overrides the API URL. Tests point this at a local server.
	Endpoint string
}

func New(cfg Config) *Source {
	countries := cfg.Countries
	if len(countries) == 0 {
		countries = []string{"US"}
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = adsArchiveEndpoint
	}
	return &Source{
		client:    cfg.Client,
		ledger:    cfg.Ledger,
		token:     strings.TrimSpace(cfg.Token),
		countries: countries,
		endpoint:  endpoint,
		log:       cfg.Logger,
		runID:     cfg.RunID,
	}
}

func (s *Source) Name() model.SignalSource { return model.SourceMetaAds }

// Enabled reports whether the source can run. A missing token means it stands
// down; the manual path covers the same signal.
func (s *Source) Enabled() bool {
	return s.token != "" && s.client != nil && s.ledger != nil
}

// Collect looks for active ads run by a business.
//
// A business with no name yields nothing: the API has nothing to search on.
// Any error is returned for the caller to log and skip — an unreliable source
// must never fail a run.
func (s *Source) Collect(ctx context.Context, b *model.Business) ([]model.Signal, error) {
	if !s.Enabled() {
		return nil, nil
	}
	name := strings.TrimSpace(b.Name)
	if name == "" {
		return nil, nil
	}

	ads, err := s.search(ctx, name, s.countriesFor(b))
	if err != nil {
		return nil, err
	}

	matched, pages := matchAds(name, ads)

	// A negative result is recorded as well as a positive one, so "we looked
	// and found nothing" stays distinguishable from "we never looked". It is
	// recorded at the same low confidence, because absence from an incomplete
	// archive is weak evidence.
	value := "false"
	if matched > 0 {
		value = "true"
	}

	detail := map[string]any{
		"matched_ads":     matched,
		"matched_pages":   pages,
		"searched_name":   name,
		"countries":       s.countriesFor(b),
		"match_method":    "page name, not website",
		"coverage_caveat": "Ad Library coverage of non-EU commercial ads is incomplete; confirm in the web UI",
	}

	s.log.Debug("meta ad library checked",
		"business_id", b.ID, "name", name, "matched", matched, "value", value)

	return []model.Signal{{
		BusinessID: b.ID,
		Source:     model.SourceMetaAds,
		Type:       model.TypeRunningAds,
		Value:      value,
		Confidence: apiConfidence,
		Detail:     detailJSON(detail),
	}}, nil
}

// countriesFor prefers the business's own country, since an Austin agency's ads
// reach the US and searching the wrong market returns nothing.
func (s *Source) countriesFor(b *model.Business) []string {
	if c := strings.ToUpper(strings.TrimSpace(b.Country)); len(c) == 2 {
		return []string{c}
	}
	return s.countries
}

func (s *Source) search(ctx context.Context, name string, countries []string) ([]ad, error) {
	params := url.Values{}
	params.Set("search_terms", name)
	params.Set("ad_type", "ALL")
	// Only ads currently running answer the question being asked.
	params.Set("ad_active_status", "ACTIVE")
	params.Set("ad_reached_countries", "["+quotedList(countries)+"]")
	params.Set("fields", "id,page_name,ad_delivery_start_time,ad_snapshot_url")
	params.Set("limit", fmt.Sprint(maxResults))

	// The token goes in a header rather than the query string, which the Graph
	// API accepts. That keeps the secret out of URLs — and therefore out of
	// logs, proxies and the cache key — so rotating a token does not orphan
	// every cached answer. The query itself fully identifies the request.
	requestURL := s.endpoint + "?" + params.Encode()

	sku := quota.SKUAdsArchive
	resp, err := s.client.API(ctx, httpx.APIRequest{
		Method: http.MethodGet,
		URL:    requestURL,
		Headers: map[string]string{
			"Authorization": "Bearer " + s.token,
		},
		BeforeNetwork: func() error {
			// The Ad Library is not billed per call, so this cap is about
			// staying under Meta's rate limiting rather than avoiding a charge.
			return s.ledger.Check(ctx, sku, 1)
		},
		AfterNetwork: func() {
			if err := s.ledger.Record(ctx, sku, "meta:ads_archive", false, s.runID); err != nil {
				s.log.Error("could not record ad library call", "error", err)
			}
		},
	})
	if err != nil {
		var statusErr *httpx.StatusError
		if errors.As(err, &statusErr) {
			return nil, apiError(statusErr.Status, statusErr.Body)
		}
		return nil, err
	}

	if resp.FromCache {
		if err := s.ledger.Record(ctx, sku, "meta:ads_archive", true, s.runID); err != nil {
			s.log.Debug("could not record cache hit", "error", err)
		}
	}
	if resp.Status != http.StatusOK {
		return nil, apiError(resp.Status, resp.Body)
	}

	var parsed archiveResponse
	if err := json.Unmarshal(resp.Body, &parsed); err != nil {
		return nil, fmt.Errorf("decode ad library response: %w", err)
	}
	return parsed.Data, nil
}

// matchAds counts ads whose advertiser plausibly is this business, and returns
// the distinct page names matched.
//
// Matching normalizes both sides and requires equality or containment. The
// containment rule is what catches "Acme Recruiting" advertising as "Acme
// Recruiting Ltd", and it is also the rule most likely to produce a false
// positive — which is why the resulting signal is low-confidence and the
// matched page names are recorded for a human to judge.
func matchAds(businessName string, ads []ad) (int, []string) {
	want := dedup.NormalizeName(businessName)
	if want == "" {
		return 0, nil
	}

	var (
		matched int
		pages   []string
		seen    = make(map[string]bool)
	)
	for _, a := range ads {
		page := dedup.NormalizeName(a.PageName)
		if page == "" {
			continue
		}
		if page != want && !strings.Contains(page, want) && !strings.Contains(want, page) {
			continue
		}
		matched++
		if !seen[a.PageName] {
			seen[a.PageName] = true
			pages = append(pages, a.PageName)
		}
	}
	return matched, pages
}

// apiError distinguishes the failures that have different fixes.
func apiError(status int, body []byte) error {
	var payload struct {
		Error struct {
			Message   string `json:"message"`
			Type      string `json:"type"`
			Code      int    `json:"code"`
			Subcode   int    `json:"error_subcode"`
			UserTitle string `json:"error_user_title"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &payload)

	msg := strings.TrimSpace(payload.Error.Message)
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}

	switch {
	case status == http.StatusUnauthorized || payload.Error.Code == 190:
		return fmt.Errorf("meta rejected the access token: %s\n"+
			"Ad Library tokens expire. Regenerate one and update META_ADS_ACCESS_TOKEN, "+
			"or leave the source disabled and record ad status with 'prospect signal'", msg)
	case status == http.StatusTooManyRequests || payload.Error.Code == 4 || payload.Error.Code == 17:
		return fmt.Errorf("meta rate limited the request: %s\n"+
			"Lower PROSPECT_WORKERS or wait; this API is free but throttled", msg)
	case status == http.StatusBadRequest:
		return fmt.Errorf("meta rejected the request: %s", msg)
	default:
		return fmt.Errorf("meta ad library returned %d: %s", status, msg)
	}
}

func quotedList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, v := range values {
		quoted = append(quoted, `"`+strings.ToUpper(strings.TrimSpace(v))+`"`)
	}
	return strings.Join(quoted, ",")
}

func detailJSON(v map[string]any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
