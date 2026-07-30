package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// harness drives the real command tree against a temporary database.
//
// These are integration tests on purpose: they exercise flag parsing,
// validation, the store and the printed output together, which is where the
// wiring bugs live. Unit tests on the command bodies would miss exactly the
// mistakes that matter — a flag not plumbed through, a filter not applied.
type harness struct {
	t      *testing.T
	dbPath string
	env    map[string]string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()

	// A pristine environment: no developer's real keys or paths leak in, and
	// no test can accidentally reach a paid API.
	for _, k := range []string{
		"PROSPECT_DB_PATH", "PROSPECT_CACHE_DB_PATH", "PROSPECT_LOG_LEVEL",
		"PROSPECT_LOG_FORMAT", "PROSPECT_USER_AGENT_EMAIL", "PROSPECT_HTTP_TIMEOUT",
		"PROSPECT_CACHE_TTL", "PROSPECT_WORKERS", "PROSPECT_RATE_PER_HOST",
		"PROSPECT_WEIGHTS_PATH", "GOOGLE_PLACES_API_KEY", "PROSPECT_PLACES_MONTHLY_MAX",
		"PROSPECT_ALLOW_PAID_APIS", "PROSPECT_QUOTA_SAFETY_MARGIN",
		"META_ADS_ACCESS_TOKEN", "PROSPECT_ENABLE_META_ADS", "PROSPECT_META_ADS_COUNTRIES",
	} {
		t.Setenv(k, "")
	}

	h := &harness{t: t, dbPath: filepath.Join(dir, "test.db"), env: map[string]string{
		"PROSPECT_CACHE_DB_PATH":    filepath.Join(dir, "cache.db"),
		"PROSPECT_USER_AGENT_EMAIL": "tests@example.com",
		"PROSPECT_RATE_PER_HOST":    "0s",
		"PROSPECT_LOG_LEVEL":        "error",
	}}
	return h
}

// run executes one command and returns its stdout.
func (h *harness) run(args ...string) (string, error) {
	h.t.Helper()

	for k, v := range h.env {
		h.t.Setenv(k, v)
	}

	var out bytes.Buffer
	root := newRootCmd("test")
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"--db", h.dbPath, "--env-file", filepath.Join(h.t.TempDir(), "absent")}, args...))

	err := root.ExecuteContext(context.Background())
	return out.String(), err
}

// mustRun fails the test if the command errors.
func (h *harness) mustRun(args ...string) string {
	h.t.Helper()
	out, err := h.run(args...)
	if err != nil {
		h.t.Fatalf("prospect %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// writeCSV creates a seed file and returns its path.
func (h *harness) writeCSV(content string) string {
	h.t.Helper()
	path := filepath.Join(h.t.TempDir(), "businesses.csv")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		h.t.Fatalf("write CSV: %v", err)
	}
	return path
}

// prospectSite serves a business site with the profile the tool is looking for:
// a contact form, a stated turnaround, a public email, no automation.
func prospectSite(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			fmt.Fprint(w, "User-agent: *\nAllow: /\n")
		case "/":
			fmt.Fprint(w, `<html><body><h1>Acme Recruiting</h1>
				<p>We place engineers with Austin startups. We respond within 24 hours.</p>
				<a href="/contact">Contact us</a></body></html>`)
		case "/contact":
			fmt.Fprint(w, `<html><body>
				<form action="/enquiry" method="post" id="contact-form">
					<input type="email" name="email"><textarea name="message"></textarea>
				</form>
				<a href="mailto:hello@acmerecruiting.com">hello@acmerecruiting.com</a>
				</body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The workflow the README documents, start to finish. If this breaks, the
// quickstart in the README is wrong.
func TestZeroKeyWorkflow(t *testing.T) {
	h := newHarness(t)
	site := prospectSite(t)

	csv := h.writeCSV("name,website,city\nAcme Recruiting," + site.URL + ",Austin\n")

	// seed
	out := h.mustRun("seed", "--csv", csv)
	if !strings.Contains(out, "1 new") {
		t.Fatalf("seed did not import the row:\n%s", out)
	}

	// enrich
	out = h.mustRun("enrich", "--all-pending")
	if !strings.Contains(out, "1 enriched") {
		t.Fatalf("enrich found nothing:\n%s", out)
	}

	// score — with no ad signal this should be low-confidence and flagged
	out = h.mustRun("score")
	if !strings.Contains(out, "Scored 1 business") {
		t.Fatalf("score did not run:\n%s", out)
	}
	if !strings.Contains(out, "need a manual ad check") {
		t.Errorf("a business with no ad signal should be flagged:\n%s", out)
	}

	// list — the extracted email should have been promoted onto the row
	out = h.mustRun("list")
	if !strings.Contains(out, "Acme Recruiting") {
		t.Fatalf("list is missing the business:\n%s", out)
	}
	if !strings.Contains(out, "hello@acmerecruiting.com") {
		t.Errorf("the extracted email was not promoted onto the business:\n%s", out)
	}
	if !strings.Contains(out, "ad?") {
		t.Errorf("list should flag the pending ad check:\n%s", out)
	}

	// brief — the reasoning and the next action must both be present
	out = h.mustRun("brief")
	for _, want := range []string{
		"Acme Recruiting",
		"contact form with no CRM or scheduling tool behind it",
		"We respond within 24 hours",
		"Ad check pending",
		"--type running_ads",
	} {
		if !strings.Contains(flatten(out), want) {
			t.Errorf("brief is missing %q:\n%s", want, out)
		}
	}

	// record the ad signal and rescore: the score must rise and the flag clear
	before := scoreFromList(t, h.mustRun("list"))
	h.mustRun("signal", "1", "--type", "running_ads", "--value", "true", "--note", "checked ad library")
	h.mustRun("score")

	after := scoreFromList(t, h.mustRun("list"))
	if after <= before {
		t.Errorf("score did not rise after recording ads: %d → %d", before, after)
	}
	if out := h.mustRun("list"); strings.Contains(out, "ad?") {
		t.Errorf("the ad flag should have cleared:\n%s", out)
	}

	// status transitions, then brief should drop the contacted business
	h.mustRun("status", "1", "--set", "emailed_1", "--note", "sent form observation angle")
	out = h.mustRun("status", "1")
	if !strings.Contains(out, "emailed_1") || !strings.Contains(out, "sent form observation angle") {
		t.Errorf("status history is missing the transition:\n%s", out)
	}
	out = h.mustRun("brief")
	if strings.Contains(out, "Acme Recruiting") {
		t.Errorf("brief should skip a contacted prospect:\n%s", out)
	}

	// export carries the reasoning
	out = h.mustRun("export", "--format", "csv")
	if !strings.Contains(out, "explanation") || !strings.Contains(out, "running paid ads") {
		t.Errorf("export lost the reasoning:\n%s", out)
	}
}

// flatten collapses all whitespace, so an assertion on a sentence does not
// depend on where the terminal wrap happened to fall.
func flatten(s string) string { return strings.Join(strings.Fields(s), " ") }

// scoreFromList reads the score column out of the first data row.
func scoreFromList(t *testing.T, out string) int {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] == "ID" {
			continue
		}
		var score int
		if _, err := fmt.Sscanf(fields[1], "%d", &score); err == nil {
			return score
		}
	}
	t.Fatalf("no score found in list output:\n%s", out)
	return 0
}

// Suppression has to hold across every command that could surface a business.
func TestSuppressionHoldsEverywhere(t *testing.T) {
	h := newHarness(t)
	site := prospectSite(t)

	csv := h.writeCSV("name,website,city\nAcme Recruiting," + site.URL + ",Austin\n")
	h.mustRun("seed", "--csv", csv)
	h.mustRun("enrich", "--all-pending")
	h.mustRun("score")

	out := h.mustRun("suppress", "1", "--reason", "asked to be removed")
	if !strings.Contains(out, "Suppressed") {
		t.Fatalf("suppress did not report:\n%s", out)
	}

	for _, cmd := range [][]string{{"list"}, {"brief"}, {"export", "--format", "csv"}} {
		out := h.mustRun(cmd...)
		if strings.Contains(out, "Acme Recruiting") {
			t.Errorf("%v still shows a suppressed business:\n%s", cmd, out)
		}
	}

	// It must not be revivable, and re-seeding must not recreate it.
	if _, err := h.run("status", "1", "--set", "emailed_1"); err == nil {
		t.Error("a suppressed business should not be movable back into the pipeline")
	}
	out = h.mustRun("seed", "--csv", csv)
	if !strings.Contains(out, "skipped (suppressed)") {
		t.Errorf("re-seeding recreated a suppressed business:\n%s", out)
	}
}

// robots.txt is honored, and the refusal is recorded rather than silent.
func TestEnrichHonorsRobots(t *testing.T) {
	h := newHarness(t)

	var fetched bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			fmt.Fprint(w, "User-agent: *\nDisallow: /\n")
			return
		}
		fetched = true
		fmt.Fprint(w, "<html><body>secret</body></html>")
	}))
	defer srv.Close()

	csv := h.writeCSV("name,website,city\nBlocked Co," + srv.URL + ",Austin\n")
	h.mustRun("seed", "--csv", csv)

	out := h.mustRun("enrich", "--all-pending")
	if fetched {
		t.Error("a disallowed site was fetched anyway")
	}
	if !strings.Contains(out, "skipped (robots.txt)") {
		t.Errorf("the robots refusal was not reported:\n%s", out)
	}
}

// Commands that fetch must refuse without a contact address; commands that do
// not fetch must work without one.
func TestContactEmailGate(t *testing.T) {
	h := newHarness(t)
	h.env["PROSPECT_USER_AGENT_EMAIL"] = ""

	csv := h.writeCSV("name,website,city\nAcme,https://acme.com,Austin\n")
	h.mustRun("seed", "--csv", csv) // no fetching, so no address needed
	h.mustRun("score")
	h.mustRun("list")

	_, err := h.run("enrich", "--all-pending")
	if err == nil {
		t.Fatal("enrich should refuse without a contact address")
	}
	if !strings.Contains(err.Error(), "PROSPECT_USER_AGENT_EMAIL") {
		t.Errorf("error should name the missing variable: %v", err)
	}
}

// Paid discovery must stand down cleanly with no key rather than failing.
func TestDiscoverWithoutKey(t *testing.T) {
	h := newHarness(t)

	out, err := h.run("discover", "--niche", "recruiting agency", "--location", "Austin, TX")
	if err != nil {
		t.Fatalf("discover without a key should exit cleanly: %v", err)
	}
	if !strings.Contains(out, "not configured") {
		t.Errorf("output should explain the source is unconfigured:\n%s", out)
	}
	// It must point at the path that needs no key.
	if !strings.Contains(out, "seed --csv") {
		t.Errorf("output should point at the zero-key path:\n%s", out)
	}
}

// A dry run must price the work without recording any usage.
func TestDiscoverDryRunSpendsNothing(t *testing.T) {
	h := newHarness(t)
	h.env["GOOGLE_PLACES_API_KEY"] = "fake-key-not-used"

	out := h.mustRun("discover", "--niche", "x", "--location", "y", "--limit", "50", "--dry-run")
	for _, want := range []string{"text_search_enterprise", "places.websiteUri", "No calls were made"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run output is missing %q:\n%s", want, out)
		}
	}

	quota := h.mustRun("quota")
	if !strings.Contains(quota, "free tier only") {
		t.Errorf("quota should report the free-tier posture:\n%s", quota)
	}
	// Nothing may have been counted.
	for _, line := range strings.Split(quota, "\n") {
		if strings.Contains(line, "text_search_enterprise") && !strings.Contains(line, " 0 ") {
			t.Errorf("a dry run recorded usage: %s", line)
		}
	}
}

// The paid switch is the only thing that lifts a ceiling past the free tier.
func TestQuotaClampWithoutOptIn(t *testing.T) {
	h := newHarness(t)
	h.env["PROSPECT_PLACES_MONTHLY_MAX"] = "100000"

	out := h.mustRun("quota")
	if !strings.Contains(flatten(out), "exceeds the free allowance") {
		t.Errorf("an over-free ceiling should be reported:\n%s", out)
	}
	if !strings.Contains(flatten(out), "capped at its own free tier") {
		t.Errorf("the note should say each SKU is capped individually:\n%s", out)
	}
	if !strings.Contains(out, "PROSPECT_ALLOW_PAID_APIS") {
		t.Errorf("the clamp note should name the opt-in:\n%s", out)
	}

	h.env["PROSPECT_ALLOW_PAID_APIS"] = "true"
	out = h.mustRun("quota")
	if !strings.Contains(out, "PAID CALLS ENABLED") {
		t.Errorf("with the opt-in set, quota should say so:\n%s", out)
	}
}

// --with-meta-ads without credentials must fail loudly and name the manual
// alternative, not silently pretend ads were checked.
func TestMetaAdsFlagRequiresCredentials(t *testing.T) {
	h := newHarness(t)
	csv := h.writeCSV("name,website,city\nAcme,https://acme.invalid,Austin\n")
	h.mustRun("seed", "--csv", csv)

	_, err := h.run("enrich", "--all-pending", "--with-meta-ads")
	if err == nil {
		t.Fatal("--with-meta-ads without a token should be refused")
	}
	if !strings.Contains(err.Error(), "prospect signal") {
		t.Errorf("the error should give the manual alternative: %v", err)
	}
}

// Bad input should be rejected with a message that says what to write.
func TestInputValidation(t *testing.T) {
	h := newHarness(t)
	csv := h.writeCSV("name,website,city\nAcme,https://acme.invalid,Austin\n")
	h.mustRun("seed", "--csv", csv)

	tests := []struct {
		name    string
		args    []string
		wantMsg string
	}{
		{"unknown signal type", []string{"signal", "1", "--type", "running_add", "--value", "true"}, "unknown signal type"},
		{"non-boolean ad value", []string{"signal", "1", "--type", "running_ads", "--value", "yes"}, "must be true or false"},
		{"bad size band", []string{"signal", "1", "--type", "size_band", "--value", "huge"}, "micro|smb|mid|large"},
		{"unknown status", []string{"status", "1", "--set", "emailed_9"}, "unknown status"},
		{"bad export format", []string{"export", "--format", "xml"}, "want csv or json"},
		{"bad business id", []string{"status", "abc"}, "not a positive integer"},
		{"missing business", []string{"signal", "999", "--type", "running_ads", "--value", "true"}, "not found"},
		{"both enrich selectors", []string{"enrich", "--all-pending", "--business-id", "1"}, "mutually exclusive"},
		{"bad month", []string{"quota", "--month", "2026-13"}, "want YYYY-MM"},
		{"missing csv", []string{"seed", "--csv", "/nonexistent/x.csv"}, "no such file"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.run(tc.args...)
			if err == nil {
				t.Fatalf("prospect %s should have failed", strings.Join(tc.args, " "))
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q does not mention %q", err, tc.wantMsg)
			}
		})
	}
}

// Empty results should explain themselves rather than printing nothing.
func TestEmptyStatesAreExplained(t *testing.T) {
	h := newHarness(t)

	tests := []struct {
		args []string
		want string
	}{
		{[]string{"list"}, "Run 'prospect score'"},
		{[]string{"brief"}, "seed and enrich"},
		{[]string{"score"}, "No businesses to score"},
		{[]string{"enrich", "--all-pending"}, "Nothing to enrich"},
		{[]string{"quota"}, "No API calls recorded"},
	}
	for _, tc := range tests {
		out := h.mustRun(tc.args...)
		if !strings.Contains(out, tc.want) {
			t.Errorf("prospect %s should explain the empty result (want %q):\n%s",
				strings.Join(tc.args, " "), tc.want, out)
		}
	}
}

// A custom weights file must actually be used, and change the outcome.
func TestCustomWeightsAreHonored(t *testing.T) {
	h := newHarness(t)
	site := prospectSite(t)

	csv := h.writeCSV("name,website,city\nAcme Recruiting," + site.URL + ",Austin\n")
	h.mustRun("seed", "--csv", csv)
	h.mustRun("enrich", "--all-pending")
	h.mustRun("score")
	baseline := scoreFromList(t, h.mustRun("list"))

	// Drop the contact-form rule to nothing and the score must fall.
	weights := h.mustRun("score", "--print-config")
	retuned := strings.Replace(weights, "points: 20", "points: 1", 1)
	path := filepath.Join(t.TempDir(), "weights.yaml")
	if err := os.WriteFile(path, []byte(retuned), 0o600); err != nil {
		t.Fatalf("write weights: %v", err)
	}

	h.mustRun("score", "--config", path)
	if got := scoreFromList(t, h.mustRun("list")); got >= baseline {
		t.Errorf("score %d did not fall from %d after de-weighting the form rule", got, baseline)
	}
}

// A broken weights file must fail at load with a message pointing at the file,
// not score everything as zero.
func TestBadWeightsFileIsRejected(t *testing.T) {
	h := newHarness(t)
	csv := h.writeCSV("name,website,city\nAcme,https://acme.invalid,Austin\n")
	h.mustRun("seed", "--csv", csv)

	path := filepath.Join(t.TempDir(), "weights.yaml")
	if err := os.WriteFile(path, []byte("rules:\n  - id: typo\n    points: 5\n    explain: x\n    when:\n      signal: not_a_real_signal\n      equals: \"true\"\n"), 0o600); err != nil {
		t.Fatalf("write weights: %v", err)
	}

	_, err := h.run("score", "--config", path)
	if err == nil {
		t.Fatal("a weights file naming an unknown signal should be rejected")
	}
	if !strings.Contains(err.Error(), "not_a_real_signal") {
		t.Errorf("error should name the offending signal: %v", err)
	}
}

// Re-running the pipeline must be idempotent: same input, no churn.
func TestPipelineIsIdempotent(t *testing.T) {
	h := newHarness(t)
	site := prospectSite(t)

	csv := h.writeCSV("name,website,city\nAcme Recruiting," + site.URL + ",Austin\n")
	h.mustRun("seed", "--csv", csv)
	h.mustRun("enrich", "--all-pending")

	out := h.mustRun("seed", "--csv", csv)
	if !strings.Contains(out, "0 new") || !strings.Contains(out, "0 updated") {
		t.Errorf("a repeat seed should be a no-op:\n%s", out)
	}

	// enrich without --force should have nothing left to do
	out = h.mustRun("enrich", "--all-pending")
	if !strings.Contains(out, "Nothing to enrich") {
		t.Errorf("a repeat enrich should find nothing pending:\n%s", out)
	}
}

// --help must work with no database and no configuration at all.
func TestHelpNeedsNothing(t *testing.T) {
	h := newHarness(t)
	h.env["PROSPECT_USER_AGENT_EMAIL"] = ""

	out := h.mustRun("--help")
	for _, cmd := range []string{"seed", "discover", "enrich", "signal", "score",
		"list", "export", "status", "suppress", "brief", "quota"} {
		if !strings.Contains(out, cmd) {
			t.Errorf("--help does not list %q", cmd)
		}
	}
}

// The signal vocabulary in help must come from the registry, so it cannot drift
// from what scoring accepts.
func TestSignalHelpListsTheVocabulary(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("signal", "--help")
	for _, typ := range []string{
		"running_ads", "contact_form", "chat_widget", "automation_tag",
		"enterprise_marker", "response_time_hours", "contact_email",
		"hiring_lead_role", "size_band", "robots_disallowed",
	} {
		if !strings.Contains(out, typ) {
			t.Errorf("signal --help does not document %q", typ)
		}
	}
}
