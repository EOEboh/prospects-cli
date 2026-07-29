package website

import (
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/net/html"

	"github.com/EOEboh/prospects-cli/internal/model"
)

// Findings is what one page yielded. Several pages are merged before anything
// is written, so a contact form found on /contact counts for the business even
// though the homepage had none.
type Findings struct {
	Emails           []string
	ContactForm      bool
	FormAction       string
	FormFields       []string
	ChatWidgets      []string
	AutomationTags   []tagSignature
	EnterpriseMarks  []tagSignature
	ResponseHours    int
	ResponsePhrase   string
	ContactPageLinks []string
}

// Extract pulls every signal this tool understands out of one page.
//
// pageURL resolves relative links; the raw body is matched for third-party
// fingerprints, since those live in attributes and inline script that a text
// extraction would discard.
func Extract(pageURL string, body []byte) (*Findings, error) {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}

	base, err := url.Parse(pageURL)
	if err != nil {
		base = nil
	}

	f := &Findings{}
	lowerHTML := strings.ToLower(string(body))

	f.AutomationTags = matchSignatures(lowerHTML, automationSignatures)
	f.EnterpriseMarks = matchSignatures(lowerHTML, enterpriseSignatures)
	for _, sig := range matchSignatures(lowerHTML, chatSignatures) {
		f.ChatWidgets = append(f.ChatWidgets, sig.Value)
	}

	var text strings.Builder
	walk(doc, base, f, &text)

	f.Emails = dedupeStrings(append(f.Emails, findEmailsInText(text.String())...))
	f.Emails = filterBusinessEmails(f.Emails)
	sort.Strings(f.Emails)
	f.ChatWidgets = dedupeStrings(f.ChatWidgets)
	f.ContactPageLinks = dedupeStrings(f.ContactPageLinks)

	f.ResponseHours, f.ResponsePhrase = findResponsePromise(text.String())
	return f, nil
}

func matchSignatures(lowerHTML string, signatures []tagSignature) []tagSignature {
	var found []tagSignature
	for _, sig := range signatures {
		for _, needle := range sig.Needles {
			if strings.Contains(lowerHTML, strings.ToLower(needle)) {
				found = append(found, sig)
				break
			}
		}
	}
	return found
}

// walk traverses the document once, collecting links, forms and visible text.
func walk(n *html.Node, base *url.URL, f *Findings, text *strings.Builder) {
	if n.Type == html.ElementNode {
		switch n.Data {
		case "a":
			handleAnchor(n, base, f)
		case "form":
			handleForm(n, base, f)
		case "script", "style", "noscript", "template":
			// Inline code is matched against the raw HTML instead; including it
			// here would flood the text with false email and phrase matches.
			return
		}
	}

	if n.Type == html.TextNode {
		text.WriteString(n.Data)
		text.WriteByte(' ')
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, base, f, text)
	}
}

func handleAnchor(n *html.Node, base *url.URL, f *Findings) {
	href := strings.TrimSpace(attr(n, "href"))
	if href == "" {
		return
	}

	if strings.HasPrefix(strings.ToLower(href), "mailto:") {
		addr := strings.TrimPrefix(strings.TrimPrefix(href, "mailto:"), "MAILTO:")
		if i := strings.IndexAny(addr, "?#"); i >= 0 {
			addr = addr[:i]
		}
		if addr, err := url.QueryUnescape(addr); err == nil {
			f.Emails = append(f.Emails, strings.ToLower(strings.TrimSpace(addr)))
		}
		return
	}

	// Contact-page candidates: judged on the link text and the path together,
	// since "Get in touch" often points at /connect.
	label := strings.ToLower(nodeText(n))
	if looksLikeContactLink(label, href) {
		if resolved := resolveURL(base, href); resolved != "" {
			f.ContactPageLinks = append(f.ContactPageLinks, resolved)
		}
	}
}

func handleForm(n *html.Node, base *url.URL, f *Findings) {
	fields := formFieldTypes(n)

	// A form is a contact form if it takes a message or an email address.
	// Search boxes and newsletter-only signups are excluded: the pitch is about
	// handling inbound enquiries, and a search box is not one.
	hasTextarea := contains(fields, "textarea")
	hasEmail := contains(fields, "email")
	action := strings.ToLower(attr(n, "action"))
	looksContactish := strings.Contains(action, "contact") ||
		strings.Contains(strings.ToLower(attr(n, "id")), "contact") ||
		strings.Contains(strings.ToLower(attr(n, "class")), "contact")

	if !hasTextarea && !(hasEmail && looksContactish) {
		return
	}
	if isSearchForm(n, fields) {
		return
	}

	f.ContactForm = true
	if f.FormAction == "" {
		if resolved := resolveURL(base, attr(n, "action")); resolved != "" {
			f.FormAction = resolved
		} else if base != nil {
			// An action-less form posts back to the page it sits on.
			f.FormAction = base.String()
		}
	}
	f.FormFields = dedupeStrings(append(f.FormFields, fields...))
}

func isSearchForm(n *html.Node, fields []string) bool {
	if contains(fields, "search") {
		return true
	}
	role := strings.ToLower(attr(n, "role"))
	id := strings.ToLower(attr(n, "id")) + " " + strings.ToLower(attr(n, "class"))
	return role == "search" || strings.Contains(id, "search")
}

func formFieldTypes(form *html.Node) []string {
	var fields []string
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "input":
				t := strings.ToLower(strings.TrimSpace(attr(n, "type")))
				if t == "" {
					t = "text"
				}
				fields = append(fields, t)
				// A field named "email" is an email field whatever its type
				// attribute claims.
				name := strings.ToLower(attr(n, "name") + " " + attr(n, "id"))
				if strings.Contains(name, "email") {
					fields = append(fields, "email")
				}
			case "textarea":
				fields = append(fields, "textarea")
			case "select":
				fields = append(fields, "select")
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(form)
	return dedupeStrings(fields)
}

var contactWords = []string{
	"contact", "get in touch", "getintouch", "reach us", "reach out",
	"enquir", "inquir", "request a quote", "get a quote", "free quote",
	"book a call", "book a demo", "schedule a call", "talk to us",
	"work with us", "hire us", "let's talk", "lets talk", "connect with us",
}

var contactPaths = []string{
	"/contact", "/contact-us", "/contactus", "/get-in-touch", "/enquiry",
	"/enquiries", "/inquiry", "/quote", "/request-quote", "/book", "/booking",
	"/connect", "/reach-us", "/talk-to-us", "/hire", "/work-with-us",
}

func looksLikeContactLink(label, href string) bool {
	for _, w := range contactWords {
		if strings.Contains(label, w) {
			return true
		}
	}
	lowerHref := strings.ToLower(href)
	for _, p := range contactPaths {
		if strings.Contains(lowerHref, p) {
			return true
		}
	}
	return false
}

// emailPattern is deliberately conservative. A loose pattern picks up
// "image@2x.png" and JavaScript fragments, and a wrong email in a cold email is
// worse than none.
var emailPattern = regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,63}`)

func findEmailsInText(text string) []string {
	var out []string
	for _, m := range emailPattern.FindAllString(text, -1) {
		out = append(out, strings.ToLower(strings.Trim(m, ".,;:")))
	}
	return out
}

// nonBusinessEmailMarkers are addresses that exist but are not a way to reach
// the business: platform noise, autoresponders, placeholders.
var nonBusinessEmailMarkers = []string{
	"noreply", "no-reply", "donotreply", "do-not-reply",
	"example.com", "example.org", "domain.com", "yourdomain",
	"email@address", "your@email", "user@", "name@",
	"sentry.io", "wixpress.com", "godaddy.com", "squarespace.com",
	"@2x", "@3x", "sentry-next", "wordpress.org", "w3.org", "schema.org",
}

var imageExtensions = []string{".png", ".jpg", ".jpeg", ".gif", ".svg", ".webp", ".ico", ".css", ".js"}

func filterBusinessEmails(emails []string) []string {
	var out []string
	for _, e := range emails {
		if len(e) > 254 || strings.Count(e, "@") != 1 {
			continue
		}
		skip := false
		for _, marker := range nonBusinessEmailMarkers {
			if strings.Contains(e, marker) {
				skip = true
				break
			}
		}
		for _, ext := range imageExtensions {
			if strings.HasSuffix(e, ext) {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, e)
		}
	}
	return out
}

// responsePatterns capture a stated turnaround. The promise itself is the
// signal: a business advertising "within 24 hours" has committed to a manual
// process slow enough to be worth automating, and the sentence is quotable
// straight back at them.
var responsePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:respond|reply|get back to you|be in touch|contact you)[^.!?]{0,40}?within\s+(\d+)\s*(hour|hr|business day|working day|day)s?`),
	regexp.MustCompile(`(?i)within\s+(\d+)\s*(hour|hr|business day|working day|day)s?[^.!?]{0,40}?(?:respond|reply|get back to you|be in touch)`),
	regexp.MustCompile(`(?i)(?:response|reply|turnaround)\s+(?:time\s+)?(?:of\s+|is\s+|:\s*)?(\d+)\s*(hour|hr|business day|working day|day)s?`),
	regexp.MustCompile(`(?i)(?:respond|reply|get back to you)[^.!?]{0,30}?in\s+(\d+)\s*(hour|hr|business day|working day|day)s?`),
}

// findResponsePromise returns the promised turnaround in hours and the sentence
// that stated it. A business day counts as 24 hours: what matters is that the
// commitment is slow, not the precise arithmetic.
func findResponsePromise(text string) (int, string) {
	flat := strings.Join(strings.Fields(text), " ")

	for _, re := range responsePatterns {
		m := re.FindStringSubmatch(flat)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil || n <= 0 {
			continue
		}

		hours := n
		unit := strings.ToLower(m[2])
		if strings.Contains(unit, "day") {
			hours = n * 24
		}
		// A "within 90 days" line is boilerplate from a privacy policy, not a
		// response promise.
		if hours > 24*14 {
			continue
		}
		return hours, sentenceAround(flat, m[0])
	}
	return 0, ""
}

// sentenceAround returns the sentence containing a match, so the explanation
// can quote the business back to itself.
func sentenceAround(text, match string) string {
	i := strings.Index(text, match)
	if i < 0 {
		return match
	}
	start := strings.LastIndexAny(text[:i], ".!?")
	if start < 0 {
		start = 0
	} else {
		start++
	}
	end := strings.IndexAny(text[i:], ".!?")
	if end < 0 {
		end = len(text)
	} else {
		end += i + 1
	}
	return strings.TrimSpace(text[start:end])
}

// Signals converts findings into the rows the scoring engine reads.
func (f *Findings) Signals(businessID int64, confidence float64) []model.Signal {
	var signals []model.Signal

	add := func(t model.SignalType, value, detail string, conf float64) {
		signals = append(signals, model.Signal{
			BusinessID: businessID,
			Source:     model.SourceWebsite,
			Type:       t,
			Value:      value,
			Detail:     detail,
			Confidence: conf,
		})
	}

	if len(f.Emails) > 0 {
		add(model.TypeContactEmail, f.Emails[0], detailJSON(map[string]any{"all": f.Emails}), confidence)
	}

	if f.ContactForm {
		add(model.TypeContactForm, "true", detailJSON(map[string]any{
			"action": f.FormAction,
			"fields": f.FormFields,
		}), confidence)
	} else {
		add(model.TypeContactForm, "false", "", confidence)
	}

	if len(f.ChatWidgets) > 0 {
		add(model.TypeChatWidget, "true", detailJSON(map[string]any{"widgets": f.ChatWidgets}), confidence)
	} else {
		add(model.TypeChatWidget, "false", "", confidence)
	}

	for _, tag := range f.AutomationTags {
		add(model.TypeAutomationTag, tag.Value, detailJSON(map[string]any{"evidence": tag.Why}), confidence)
	}
	for _, tag := range f.EnterpriseMarks {
		add(model.TypeEnterpriseMarker, tag.Value, detailJSON(map[string]any{"evidence": tag.Why}), confidence)
	}

	if f.ResponseHours > 0 {
		add(model.TypeResponseTimeHours, strconv.Itoa(f.ResponseHours),
			detailJSON(map[string]any{"quote": f.ResponsePhrase}), confidence)
	}

	return signals
}

// Merge folds another page's findings into this one. Presence wins over
// absence: a form on /contact counts even when the homepage had none.
func (f *Findings) Merge(other *Findings) {
	f.Emails = dedupeStrings(append(f.Emails, other.Emails...))
	sort.Strings(f.Emails)
	f.ChatWidgets = dedupeStrings(append(f.ChatWidgets, other.ChatWidgets...))
	f.AutomationTags = mergeSignatures(f.AutomationTags, other.AutomationTags)
	f.EnterpriseMarks = mergeSignatures(f.EnterpriseMarks, other.EnterpriseMarks)
	f.ContactPageLinks = dedupeStrings(append(f.ContactPageLinks, other.ContactPageLinks...))

	if other.ContactForm && !f.ContactForm {
		f.ContactForm = true
		f.FormAction = other.FormAction
	}
	f.FormFields = dedupeStrings(append(f.FormFields, other.FormFields...))

	// The slowest stated promise is the honest one to quote.
	if other.ResponseHours > f.ResponseHours {
		f.ResponseHours = other.ResponseHours
		f.ResponsePhrase = other.ResponsePhrase
	}
}

func mergeSignatures(a, b []tagSignature) []tagSignature {
	seen := make(map[string]bool, len(a))
	out := make([]tagSignature, 0, len(a)+len(b))
	for _, list := range [][]tagSignature{a, b} {
		for _, sig := range list {
			if !seen[sig.Value] {
				seen[sig.Value] = true
				out = append(out, sig)
			}
		}
	}
	return out
}

func detailJSON(v map[string]any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, name) {
			return a.Val
		}
	}
	return ""
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func resolveURL(base *url.URL, href string) string {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") {
		return ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if base != nil {
		u = base.ResolveReference(u)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	u.Fragment = ""
	return u.String()
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
