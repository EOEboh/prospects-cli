package website

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EOEboh/prospects-cli/internal/model"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func extractFixture(t *testing.T, name, pageURL string) *Findings {
	t.Helper()
	f, err := Extract(pageURL, loadFixture(t, name))
	if err != nil {
		t.Fatalf("Extract(%s): %v", name, err)
	}
	return f
}

// The profile this tool exists to find: a contact form, a stated turnaround,
// a public email, and no automation behind any of it.
func TestExtractIdealProspect(t *testing.T) {
	f := extractFixture(t, "ideal_prospect.html", "https://acmerecruiting.com/")

	if !f.ContactForm {
		t.Error("contact form not detected")
	}
	if f.FormAction != "https://acmerecruiting.com/submit-enquiry" {
		t.Errorf("FormAction = %q, want the resolved absolute endpoint", f.FormAction)
	}
	if !contains(f.FormFields, "textarea") || !contains(f.FormFields, "email") {
		t.Errorf("FormFields = %v, want textarea and email", f.FormFields)
	}
	if len(f.Emails) != 1 || f.Emails[0] != "hello@acmerecruiting.com" {
		t.Errorf("Emails = %v, want [hello@acmerecruiting.com]", f.Emails)
	}
	if f.ResponseHours != 24 {
		t.Errorf("ResponseHours = %d, want 24", f.ResponseHours)
	}
	if !strings.Contains(f.ResponsePhrase, "24 hours") {
		t.Errorf("ResponsePhrase = %q, want the sentence quoted back", f.ResponsePhrase)
	}
	if len(f.AutomationTags) != 0 {
		t.Errorf("AutomationTags = %v, want none", f.AutomationTags)
	}
	if len(f.ChatWidgets) != 0 {
		t.Errorf("ChatWidgets = %v, want none", f.ChatWidgets)
	}

	found := false
	for _, link := range f.ContactPageLinks {
		if link == "https://acmerecruiting.com/contact" {
			found = true
		}
	}
	if !found {
		t.Errorf("ContactPageLinks = %v, want the /contact link", f.ContactPageLinks)
	}
}

// A business already running HubSpot and Calendly has solved part of the
// problem, which is exactly what the automation-tag penalty is for.
func TestExtractAlreadyAutomated(t *testing.T) {
	f := extractFixture(t, "already_automated.html", "https://brightpath.com/")

	got := make(map[string]bool)
	for _, tag := range f.AutomationTags {
		got[tag.Value] = true
	}
	for _, want := range []string{"hubspot", "calendly", "intercom"} {
		if !got[want] {
			t.Errorf("automation tag %q not detected (found %v)", want, keys(got))
		}
	}
	if !contains(f.ChatWidgets, "intercom") {
		t.Errorf("ChatWidgets = %v, want intercom", f.ChatWidgets)
	}
	if !f.ContactForm {
		t.Error("contact form not detected")
	}
}

// Enterprise markers score negatively: this business will build it in-house.
func TestExtractEnterpriseMarkers(t *testing.T) {
	f := extractFixture(t, "enterprise.html", "https://globex.com/")

	got := make(map[string]bool)
	for _, tag := range f.EnterpriseMarks {
		got[tag.Value] = true
	}
	for _, want := range []string{"salesforce", "marketo", "greenhouse", "adobe-experience"} {
		if !got[want] {
			t.Errorf("enterprise marker %q not detected (found %v)", want, keys(got))
		}
	}
}

// Absence has to be recorded as confidently as presence, or a page with
// nothing on it is indistinguishable from a page never fetched.
func TestExtractBarePage(t *testing.T) {
	f := extractFixture(t, "bare.html", "https://cedarstaffing.com/")

	if f.ContactForm {
		t.Error("a search box was mistaken for a contact form")
	}
	if len(f.Emails) != 0 {
		t.Errorf("Emails = %v, want none", f.Emails)
	}
	if f.ResponseHours != 0 {
		t.Errorf("ResponseHours = %d, want 0", f.ResponseHours)
	}
}

// A wrong address in a cold email is worse than no address.
func TestExtractFiltersNonBusinessEmails(t *testing.T) {
	f := extractFixture(t, "noisy_emails.html", "https://summitsearch.co.uk/")

	if len(f.Emails) != 1 || f.Emails[0] != "enquiries@summitsearch.co.uk" {
		t.Errorf("Emails = %v, want only [enquiries@summitsearch.co.uk]", f.Emails)
	}
}

func TestContactFormDetection(t *testing.T) {
	tests := []struct {
		name       string
		html       string
		wantForm   bool
		wantAction string
	}{
		{
			name:       "textarea makes it a contact form",
			html:       `<form action="/send"><textarea name="msg"></textarea></form>`,
			wantForm:   true,
			wantAction: "https://x.com/send",
		},
		{
			name:     "search form is not a contact form",
			html:     `<form role="search" action="/search"><input type="search" name="q"></form>`,
			wantForm: false,
		},
		{
			name:     "search by class is not a contact form",
			html:     `<form class="site-search" action="/s"><input type="text" name="q"></form>`,
			wantForm: false,
		},
		{
			name:     "newsletter email-only signup is not a contact form",
			html:     `<form action="/subscribe"><input type="email" name="email"></form>`,
			wantForm: false,
		},
		{
			name:       "email-only form IS a contact form when the action says so",
			html:       `<form action="/contact"><input type="email" name="email"></form>`,
			wantForm:   true,
			wantAction: "https://x.com/contact",
		},
		{
			name:       "action-less form posts back to its own page",
			html:       `<form id="contact"><textarea name="msg"></textarea></form>`,
			wantForm:   true,
			wantAction: "https://x.com/page",
		},
		{
			name:       "absolute action to a third party is kept",
			html:       `<form action="https://forms.hsforms.com/x"><textarea name="m"></textarea></form>`,
			wantForm:   true,
			wantAction: "https://forms.hsforms.com/x",
		},
		{
			name:     "a field named email counts even without type=email",
			html:     `<form id="contact-form" action="/c"><input type="text" name="email_address"></form>`,
			wantForm: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, err := Extract("https://x.com/page", []byte("<html><body>"+tc.html+"</body></html>"))
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if f.ContactForm != tc.wantForm {
				t.Errorf("ContactForm = %v, want %v", f.ContactForm, tc.wantForm)
			}
			if tc.wantAction != "" && f.FormAction != tc.wantAction {
				t.Errorf("FormAction = %q, want %q", f.FormAction, tc.wantAction)
			}
		})
	}
}

// The promise is quotable straight back at the business, so it has to be read
// accurately.
func TestResponseTimeExtraction(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		wantHours int
	}{
		{"plain 24 hours", "We respond within 24 hours.", 24},
		{"reply within 48 hours", "We will reply within 48 hours of your enquiry.", 48},
		{"get back to you", "We aim to get back to you within 2 business days.", 48},
		{"one business day", "We respond within 1 business day.", 24},
		{"response time phrasing", "Our typical response time is 12 hours.", 12},
		{"in N hours", "We'll get back to you in 6 hours.", 6},
		{"reordered clause", "Within 24 hours we will respond to every enquiry.", 24},
		{"abbreviated hr", "We reply within 4 hrs.", 4},

		{"no promise", "We are a friendly recruitment agency.", 0},
		{"unrelated number", "We have placed 500 candidates.", 0},
		{"privacy boilerplate is not a promise", "We retain your data and will respond within 90 days if required by law.", 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, err := Extract("https://x.com/", []byte("<html><body><p>"+tc.text+"</p></body></html>"))
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if f.ResponseHours != tc.wantHours {
				t.Errorf("ResponseHours = %d, want %d (text: %q)", f.ResponseHours, tc.wantHours, tc.text)
			}
			if tc.wantHours > 0 && f.ResponsePhrase == "" {
				t.Error("a detected promise must carry the sentence that stated it")
			}
		})
	}
}

// Script bodies are matched for fingerprints but excluded from text, or every
// inline analytics blob would produce phantom emails and phrases.
func TestScriptContentExcludedFromText(t *testing.T) {
	const page = `<html><body>
		<script>var contact = "fake@tracker.io"; // respond within 3 hours</script>
		<p>Acme Recruiting</p>
	</body></html>`

	f, err := Extract("https://x.com/", []byte(page))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(f.Emails) != 0 {
		t.Errorf("Emails = %v, want none: script content is not page text", f.Emails)
	}
	if f.ResponseHours != 0 {
		t.Errorf("ResponseHours = %d, want 0: a comment is not a promise", f.ResponseHours)
	}
}

func TestContactLinkDetection(t *testing.T) {
	tests := []struct {
		name  string
		html  string
		want  string
		found bool
	}{
		{"contact us text", `<a href="/reach">Contact Us</a>`, "https://x.com/reach", true},
		{"get in touch text", `<a href="/connect">Get in touch</a>`, "https://x.com/connect", true},
		{"path only", `<a href="/contact-us">Click</a>`, "https://x.com/contact-us", true},
		{"quote request", `<a href="/rfq">Request a quote</a>`, "https://x.com/rfq", true},
		{"unrelated link", `<a href="/blog">Our blog</a>`, "", false},
		{"fragment ignored", `<a href="#contact">Contact</a>`, "", false},
		{"mailto is not a page link", `<a href="mailto:a@b.com">Contact</a>`, "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, err := Extract("https://x.com/page", []byte("<html><body>"+tc.html+"</body></html>"))
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if tc.found {
				if !contains(f.ContactPageLinks, tc.want) {
					t.Errorf("ContactPageLinks = %v, want to contain %q", f.ContactPageLinks, tc.want)
				}
			} else if len(f.ContactPageLinks) != 0 {
				t.Errorf("ContactPageLinks = %v, want none", f.ContactPageLinks)
			}
		})
	}
}

// Findings from several pages combine: a form on /contact counts for the
// business even when the homepage had none.
func TestMergeFindings(t *testing.T) {
	home := extractFixture(t, "bare.html", "https://cedarstaffing.com/")
	contact := extractFixture(t, "ideal_prospect.html", "https://cedarstaffing.com/contact")

	home.Merge(contact)

	if !home.ContactForm {
		t.Error("a form found on the contact page must count for the business")
	}
	if home.ResponseHours != 24 {
		t.Errorf("ResponseHours = %d, want 24 from the merged page", home.ResponseHours)
	}
	if len(home.Emails) == 0 {
		t.Error("emails from the merged page were lost")
	}
}

func TestMergeKeepsTheSlowestPromise(t *testing.T) {
	a := &Findings{ResponseHours: 12, ResponsePhrase: "within 12 hours"}
	b := &Findings{ResponseHours: 48, ResponsePhrase: "within 2 business days"}
	a.Merge(b)
	if a.ResponseHours != 48 {
		t.Errorf("ResponseHours = %d, want the slowest stated promise (48)", a.ResponseHours)
	}
	if a.ResponsePhrase != "within 2 business days" {
		t.Errorf("ResponsePhrase = %q, want the phrase that matches the hours", a.ResponsePhrase)
	}
}

// Absence is recorded explicitly, so "no form" is distinguishable from "never
// looked".
func TestSignalsRecordAbsenceExplicitly(t *testing.T) {
	f := extractFixture(t, "bare.html", "https://cedarstaffing.com/")
	signals := f.Signals(7, 0.8)

	byType := make(map[model.SignalType]string)
	for _, s := range signals {
		if s.BusinessID != 7 {
			t.Errorf("signal carries business %d, want 7", s.BusinessID)
		}
		if s.Source != model.SourceWebsite {
			t.Errorf("signal source = %q, want website", s.Source)
		}
		byType[s.Type] = s.Value
	}

	if byType[model.TypeContactForm] != "false" {
		t.Errorf("contact_form = %q, want an explicit false", byType[model.TypeContactForm])
	}
	if byType[model.TypeChatWidget] != "false" {
		t.Errorf("chat_widget = %q, want an explicit false", byType[model.TypeChatWidget])
	}
	if _, ok := byType[model.TypeContactEmail]; ok {
		t.Error("no email should mean no email signal, not an empty one")
	}
}

func TestSignalsCarryEvidence(t *testing.T) {
	f := extractFixture(t, "ideal_prospect.html", "https://acmerecruiting.com/")

	for _, s := range f.Signals(1, 1.0) {
		switch s.Type {
		case model.TypeContactForm:
			if !strings.Contains(s.Detail, "submit-enquiry") {
				t.Errorf("contact_form detail = %q, want the action endpoint", s.Detail)
			}
		case model.TypeResponseTimeHours:
			if !strings.Contains(s.Detail, "24 hours") {
				t.Errorf("response_time detail = %q, want the quoted sentence", s.Detail)
			}
		}
	}
}

func TestExtractHandlesMalformedHTML(t *testing.T) {
	// html.Parse recovers from anything; the point is that we never panic.
	inputs := []string{
		"",
		"not html at all",
		"<html><body><form><textarea>",
		"<<<>>>",
		`<html><body><a href="::::">x</a></body></html>`,
	}
	for _, in := range inputs {
		if _, err := Extract("https://x.com/", []byte(in)); err != nil {
			t.Errorf("Extract(%q) = %v, want no error", in, err)
		}
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
