// Package website enriches a business by reading its public web pages.
//
// It looks at how the business handles inbound leads: whether there is a
// contact form and where it posts, whether a chat widget is staffed, which
// marketing tools are already wired up, and whether a response time is
// promised in writing.
//
// Forms are detected, never submitted. Bulk submission is spam and would get
// the operator's domain flagged; the few prospects that reach the top of the
// list are tested by hand.
package website

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/EOEboh/prospects-cli/internal/httpx"
	"github.com/EOEboh/prospects-cli/internal/model"
)

// maxContactPages caps the follow-up fetches per business. Three requests
// (homepage plus two contact pages) is enough to find a form and an address on
// almost any small-business site, and stays quiet in someone's access log.
const maxContactPages = 2

// fallbackContactPaths are tried only when the homepage links to no contact
// page of its own. Guessing is a last resort: a guessed URL is usually a 404,
// which costs the site a request for nothing.
var fallbackContactPaths = []string{"/contact", "/contact-us"}

// Source implements source.Source over a business's own website.
type Source struct {
	client *httpx.Client
	log    *slog.Logger
}

func New(client *httpx.Client, log *slog.Logger) *Source {
	return &Source{client: client, log: log}
}

func (s *Source) Name() model.SignalSource { return model.SourceWebsite }

// Enabled reports whether the source can run. The website source needs no API
// key, which is what makes the zero-key path work.
func (s *Source) Enabled() bool { return s.client != nil }

// Collect fetches a business's pages and returns what they revealed.
//
// A business with no website yields no signals and no error: that is a fact
// about the business, not a failure.
func (s *Source) Collect(ctx context.Context, b *model.Business) ([]model.Signal, error) {
	if strings.TrimSpace(b.Website) == "" {
		return nil, nil
	}

	homeURL, err := normalizeStartURL(b.Website)
	if err != nil {
		return nil, err
	}

	home, err := s.client.Get(ctx, homeURL)
	if err != nil {
		// A disallowed fetch is recorded rather than dropped, so an unreadable
		// site stays distinguishable from a site with nothing on it.
		var disallowed *httpx.ErrDisallowed
		if errors.As(err, &disallowed) {
			s.log.Info("robots.txt disallows the homepage",
				"business_id", b.ID, "url", homeURL, "rule", disallowed.Rule)
			return []model.Signal{{
				BusinessID: b.ID,
				Source:     model.SourceWebsite,
				Type:       model.TypeRobotsDisallowed,
				Value:      "true",
				Detail:     detailJSON(map[string]any{"url": homeURL, "rule": disallowed.Rule}),
				Confidence: 1.0,
			}}, nil
		}
		return nil, fmt.Errorf("fetch homepage %s: %w", homeURL, err)
	}

	if home.Status != http.StatusOK {
		return nil, fmt.Errorf("homepage %s returned %d", homeURL, home.Status)
	}
	if !home.IsHTML() {
		return nil, fmt.Errorf("homepage %s is %s, not HTML", homeURL, home.ContentType)
	}

	findings, err := Extract(home.URL, home.Body)
	if err != nil {
		return nil, fmt.Errorf("parse homepage %s: %w", homeURL, err)
	}

	pagesRead := 1
	attempted := 0
	for _, pageURL := range s.contactCandidates(home.URL, findings) {
		// The cap is on requests issued, not pages successfully read: a site
		// that 404s every guess must not be probed indefinitely.
		if attempted >= maxContactPages {
			break
		}
		attempted++

		page, err := s.client.Get(ctx, pageURL)
		if err != nil {
			// One unreachable contact page never fails the business.
			s.log.Debug("skipping contact page", "business_id", b.ID, "url", pageURL, "error", err)
			continue
		}
		if page.Status != http.StatusOK || !page.IsHTML() {
			continue
		}

		more, err := Extract(page.URL, page.Body)
		if err != nil {
			s.log.Debug("could not parse contact page", "url", pageURL, "error", err)
			continue
		}
		// Only a page we actually read counts toward confidence. A 404 guess
		// is evidence of nothing.
		pagesRead++
		findings.Merge(more)
	}

	// Confidence reflects how much of the site was actually read. Signals from
	// a homepage alone are weaker evidence than signals confirmed on a contact
	// page, and the score says so rather than pretending otherwise.
	confidence := 0.7
	if pagesRead > 1 {
		confidence = 0.9
	}

	signals := findings.Signals(b.ID, confidence)
	s.log.Debug("website enriched",
		"business_id", b.ID, "url", homeURL, "pages_read", pagesRead,
		"from_cache", home.FromCache, "signals", len(signals))
	return signals, nil
}

// contactCandidates picks which follow-up pages to fetch: links the homepage
// offered first, then a small number of conventional guesses if it offered
// none.
func (s *Source) contactCandidates(homeURL string, f *Findings) []string {
	base, err := url.Parse(homeURL)
	if err != nil {
		return nil
	}

	var out []string
	seen := map[string]bool{canonical(homeURL): true}

	for _, link := range f.ContactPageLinks {
		u, err := url.Parse(link)
		if err != nil {
			continue
		}
		// Stay on the business's own site: an off-site "contact" link is
		// someone else's page.
		if !sameHost(base, u) {
			continue
		}
		key := canonical(link)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, link)
	}

	if len(out) == 0 {
		for _, path := range fallbackContactPaths {
			guess := base.Scheme + "://" + base.Host + path
			if !seen[canonical(guess)] {
				out = append(out, guess)
			}
		}
	}
	return out
}

func sameHost(a, b *url.URL) bool {
	return strings.EqualFold(strings.TrimPrefix(a.Host, "www."), strings.TrimPrefix(b.Host, "www."))
}

func canonical(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	u.Fragment = ""
	return strings.TrimSuffix(strings.ToLower(u.String()), "/")
}

// normalizeStartURL turns whatever is stored in the website column into
// something fetchable. Bare hostnames are common in hand-built CSVs.
func normalizeStartURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if !strings.Contains(s, "//") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("parse website %q: %w", raw, err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("website %q has no host", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("website %q uses unsupported scheme %q", raw, u.Scheme)
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), nil
}
