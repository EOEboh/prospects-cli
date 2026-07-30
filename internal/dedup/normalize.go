// Package dedup derives the stable keys that decide whether two records are
// the same business.
//
// The website domain is the primary key and the name plus city is the
// fallback. Both are normalized here and nowhere else, so the database's
// unique indexes and the application always agree on what "same" means.
package dedup

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
	"golang.org/x/text/unicode/norm"
)

// sharedHostSuffixes are website-builder and hosting platforms where the
// subdomain, not the registrable domain, identifies the business.
//
// Without this, every small business on Wix collapses into the single row
// "wixsite.com" and all but the first are silently discarded — exactly the
// businesses this tool exists to find, since a template site with a bare
// contact form is the ideal prospect.
//
// Platforms already on the Public Suffix List (github.io, netlify.app,
// myshopify.com, wordpress.com) resolve correctly without an entry here; the
// list is a safety net for the ones that are not.
var sharedHostSuffixes = map[string]bool{
	"wixsite.com":      true,
	"wix.com":          true,
	"squarespace.com":  true,
	"weebly.com":       true,
	"godaddysites.com": true,
	"business.site":    true,
	"square.site":      true,
	"jimdosite.com":    true,
	"jimdofree.com":    true,
	"strikingly.com":   true,
	"site123.me":       true,
	"ueniweb.com":      true,
	"yolasite.com":     true,
	"tilda.ws":         true,
	"webnode.com":      true,
	"webnode.page":     true,
	"webflow.io":       true,
	"duda.co":          true,
	"companysite.net":  true,
	"zohosites.com":    true,
	"mystrikingly.com": true,
}

// pathIdentityHosts are hosts where the business is identified by the path,
// not the host at all. A business whose only listed "website" is a Facebook
// page is common in Places data, and host-only keys would merge every one of
// them into a single row.
//
// The value is how many leading path segments belong to the identity.
var pathIdentityHosts = map[string]int{
	"facebook.com":        1,
	"instagram.com":       1,
	"linkedin.com":        2, // /company/acme
	"linktr.ee":           1,
	"sites.google.com":    2, // /view/acme
	"business.google.com": 1,
	"yelp.com":            2, // /biz/acme
	"nextdoor.com":        2,
	"medium.com":          1,
	"substack.com":        1,
}

// legalSuffixes are dropped from a business name before comparison. "Acme LLC"
// and "Acme" in the same city are one business.
//
// Lookups happen after punctuation is stripped, so entries are bare words:
// "Inc." and "Ltd." arrive here as "inc" and "ltd".
//
// Deliberately conservative: "group", "partners" and "associates" are NOT here,
// because "Acme Group" and "Acme" are plausibly different companies and a false
// merge silently destroys a prospect.
var legalSuffixes = map[string]bool{
	"inc": true, "incorporated": true,
	"llc": true, "llp": true, "lp": true,
	"ltd": true, "limited": true,
	"corp": true, "corporation": true,
	"co": true, "company": true,
	"plc": true, "gmbh": true, "ag": true, "bv": true, "nv": true,
	"pty": true, "pte": true, "srl": true, "sa": true, "sas": true,
	"pc": true, "pa": true,
}

// Domain derives the dedup key from a website. It returns an empty string with
// a nil error when there is nothing to work with, since a business without a
// website is normal and falls back to NameKey.
//
// A non-nil error means the input looked like a URL but could not be parsed,
// which is worth logging against that row.
func Domain(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}

	// Bare hostnames are common in hand-built CSVs. url.Parse puts a
	// schemeless input in Path rather than Host, so give it a scheme first.
	if !strings.Contains(s, "//") {
		s = "https://" + s
	}

	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("parse website %q: %w", raw, err)
	}

	host := strings.ToLower(u.Hostname())
	host = strings.TrimSuffix(host, ".") // fully-qualified trailing dot
	if host == "" {
		return "", fmt.Errorf("website %q has no host", raw)
	}

	// Internationalized domains are compared in their ASCII form, so the
	// unicode and punycode spellings of one site produce one key.
	if ascii, err := idna.Lookup.ToASCII(host); err == nil && ascii != "" {
		host = ascii
	}

	host = strings.TrimPrefix(host, "www.")

	// Path-identity platforms first: the host alone is not the business.
	if segments, ok := pathIdentityHosts[host]; ok {
		if key := hostWithPath(host, u.Path, segments); key != host {
			return key, nil
		}
		// A bare facebook.com with no path identifies nothing. Treat it as no
		// website rather than merging every such row together.
		return "", nil
	}

	registrable, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		// Single-label hosts and IP addresses land here. Keep the host as-is:
		// it is still a stable key, just not a registrable domain.
		return host, nil
	}

	// On a shared platform the subdomain is the business, so the registrable
	// domain is too coarse.
	if sharedHostSuffixes[registrable] {
		return host, nil
	}
	return registrable, nil
}

func hostWithPath(host, path string, segments int) string {
	parts := make([]string, 0, segments)
	for _, p := range strings.Split(path, "/") {
		if p == "" {
			continue
		}
		parts = append(parts, strings.ToLower(p))
		if len(parts) == segments {
			break
		}
	}
	if len(parts) == 0 {
		return host
	}
	return host + "/" + strings.Join(parts, "/")
}

// NameKey is the fallback dedup key for businesses with no usable website.
//
// It is scoped by city because business names are only unique locally: two
// unrelated "Bright Path Talent" firms in different cities are two prospects,
// and merging them would lose one.
func NameKey(name, city string) string {
	n := NormalizeName(name)
	if n == "" {
		return ""
	}
	return n + "|" + normalizeToken(city)
}

// NormalizeName reduces a business name to its comparable form: lowercase,
// unaccented, punctuation-free, without a leading "the" or a trailing legal
// suffix.
func NormalizeName(name string) string {
	s := normalizeToken(name)
	if s == "" {
		return ""
	}

	words := strings.Fields(s)
	if len(words) > 1 && words[0] == "the" {
		words = words[1:]
	}
	// Strip repeatedly: "Acme Holdings Co Ltd" ends at "acme holdings".
	for len(words) > 1 && legalSuffixes[words[len(words)-1]] {
		words = words[:len(words)-1]
	}
	return strings.Join(words, " ")
}

// normalizeToken lowercases, strips diacritics, and reduces punctuation to
// single spaces. "Café Ampère, Inc." and "Cafe Ampere Inc" converge here.
func normalizeToken(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	s = norm.NFD.String(s)

	var b strings.Builder
	b.Grow(len(s))
	lastSpace := true // leading spaces are dropped
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Mn, r):
			// Combining mark left over from NFD: this is the accent itself.
			continue
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastSpace = false
		default:
			if !lastSpace {
				b.WriteRune(' ')
				lastSpace = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// Keys derives both dedup keys for a business at once, which is what the store
// needs on every insert.
func Keys(website, name, city string) (domain, nameKey string, err error) {
	domain, err = Domain(website)
	if err != nil {
		// A malformed website is reported but must not stop the row: the name
		// key still identifies the business.
		return "", NameKey(name, city), err
	}
	return domain, NameKey(name, city), nil
}
