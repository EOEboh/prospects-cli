package scoring

import (
	"os"
	"strings"
	"testing"

	"github.com/EOEboh/prospects-cli/internal/model"
)

func defaultConfig(t *testing.T) *Config {
	t.Helper()
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load defaults: %v", err)
	}
	return cfg
}

func sig(t model.SignalType, value string) model.Signal {
	return model.Signal{Type: t, Value: value, Source: model.SourceWebsite, Confidence: 1}
}

func sigWithDetail(t model.SignalType, value, detail string) model.Signal {
	s := sig(t, value)
	s.Detail = detail
	return s
}

func componentPoints(res Result) map[string]int {
	out := make(map[string]int, len(res.Components))
	for _, c := range res.Components {
		out[c.Rule] = c.Points
	}
	return out
}

// The default weights sum to 110 positive points, so a perfect prospect
// normalizes to 100 rather than overflowing.
func TestMaxPossibleMatchesStartingWeights(t *testing.T) {
	cfg := defaultConfig(t)
	if got := cfg.MaxPossible(); got != 110 {
		t.Errorf("MaxPossible = %d, want 110 (40+20+15+15+10+10)", got)
	}
}

// The archetype: running ads, a bare contact form, a slow promise, reachable.
func TestScoreIdealProspect(t *testing.T) {
	cfg := defaultConfig(t)

	res := cfg.Score(Input{
		Business: model.Business{ID: 1, Name: "Acme Recruiting"},
		Signals: []model.Signal{
			sig(model.TypeRunningAds, "true"),
			sig(model.TypeContactForm, "true"),
			sig(model.TypeChatWidget, "false"),
			sigWithDetail(model.TypeResponseTimeHours, "24", `{"quote":"We respond within 24 hours."}`),
			sig(model.TypeContactEmail, "hello@acmerecruiting.com"),
			sig(model.TypeSizeBand, "smb"),
		},
	})

	points := componentPoints(res)
	want := map[string]int{
		"running_ads":              40,
		"unautomated_contact_form": 20,
		"slow_stated_response":     15,
		"target_size":              10,
		"public_email":             10,
	}
	for rule, pts := range want {
		if points[rule] != pts {
			t.Errorf("rule %s contributed %d, want %d", rule, points[rule], pts)
		}
	}

	if res.Raw != 95 {
		t.Errorf("Raw = %d, want 95", res.Raw)
	}
	// 95 of 110 normalizes to 86.
	if res.Score != 86 {
		t.Errorf("Score = %d, want 86", res.Score)
	}
	if res.NeedsManualCheck {
		t.Error("ad status was recorded; no manual check should be needed")
	}
	if res.LowConfidence {
		t.Errorf("confidence %.2f flagged low despite full data", res.Confidence)
	}
}

// The compound rule is the reason a flat weight map would not do: a form only
// scores when nothing is already automating it.
func TestContactFormRuleRequiresNoAutomation(t *testing.T) {
	cfg := defaultConfig(t)

	bare := cfg.Score(Input{
		Business: model.Business{ID: 1, Name: "Bare"},
		Signals:  []model.Signal{sig(model.TypeContactForm, "true")},
	})
	if componentPoints(bare)["unautomated_contact_form"] != 20 {
		t.Error("a contact form with no automation should score 20")
	}

	automated := cfg.Score(Input{
		Business: model.Business{ID: 2, Name: "Automated"},
		Signals: []model.Signal{
			sig(model.TypeContactForm, "true"),
			sig(model.TypeAutomationTag, "hubspot"),
		},
	})
	if _, scored := componentPoints(automated)["unautomated_contact_form"]; scored {
		t.Error("a form behind HubSpot must not score: the problem is already solved")
	}

	// A form posting to a Salesforce web-to-lead endpoint is the most
	// automated form there is. Enterprise tooling has to count as automation
	// or this scores as though the form went nowhere.
	enterprise := cfg.Score(Input{
		Business: model.Business{ID: 3, Name: "Globex"},
		Signals: []model.Signal{
			sig(model.TypeContactForm, "true"),
			sig(model.TypeEnterpriseMarker, "salesforce"),
		},
	})
	if _, scored := componentPoints(enterprise)["unautomated_contact_form"]; scored {
		t.Error("a form behind Salesforce must not score as unautomated")
	}
}

// A threshold, not an equality: any promise of a day or more counts.
func TestResponseTimeThreshold(t *testing.T) {
	cfg := defaultConfig(t)

	tests := []struct {
		hours     string
		wantScore bool
	}{
		{"1", false},
		{"12", false},
		{"23", false},
		{"24", true},
		{"48", true},
		{"72", true},
		{"not a number", false},
	}
	for _, tc := range tests {
		res := cfg.Score(Input{
			Business: model.Business{ID: 1},
			Signals:  []model.Signal{sig(model.TypeResponseTimeHours, tc.hours)},
		})
		_, scored := componentPoints(res)["slow_stated_response"]
		if scored != tc.wantScore {
			t.Errorf("response_time_hours=%s scored=%v, want %v", tc.hours, scored, tc.wantScore)
		}
	}
}

// Too big to sell to: the penalty has to actually bite.
func TestEnterprisePenalty(t *testing.T) {
	cfg := defaultConfig(t)

	res := cfg.Score(Input{
		Business: model.Business{ID: 1, Name: "Globex"},
		Signals: []model.Signal{
			sig(model.TypeContactForm, "true"),
			sig(model.TypeContactEmail, "info@globex.com"),
			sig(model.TypeEnterpriseMarker, "salesforce"),
		},
	})

	if componentPoints(res)["enterprise"] != -25 {
		t.Errorf("enterprise penalty = %d, want -25", componentPoints(res)["enterprise"])
	}
	// The form does not score — Salesforce is automation — so this is
	// 10 (email) - 25 (enterprise) = -15, and clamps to 0 when printed.
	if res.Raw != -15 {
		t.Errorf("Raw = %d, want -15", res.Raw)
	}
	if res.Score != 0 {
		t.Errorf("Score = %d, want 0", res.Score)
	}
}

// A negative raw score must not print as a negative number.
func TestScoreClampsAtZero(t *testing.T) {
	cfg := defaultConfig(t)

	res := cfg.Score(Input{
		Business: model.Business{ID: 1, Name: "Globex"},
		Signals: []model.Signal{
			sig(model.TypeEnterpriseMarker, "salesforce"),
			sig(model.TypeChatWidget, "true"),
		},
	})
	if res.Raw >= 0 {
		t.Fatalf("Raw = %d, want negative for this fixture", res.Raw)
	}
	if res.Score != 0 {
		t.Errorf("Score = %d, want 0: a negative raw must clamp", res.Score)
	}
}

// Partial data still scores. Refusing to score an incompletely-known business
// would hide most of the pipeline.
func TestScoreFromPartialData(t *testing.T) {
	cfg := defaultConfig(t)

	res := cfg.Score(Input{
		Business: model.Business{ID: 1, Name: "Website Only"},
		Signals: []model.Signal{
			sig(model.TypeContactForm, "true"),
			sig(model.TypeChatWidget, "false"),
			sig(model.TypeContactEmail, "hi@example.org"),
		},
	})

	if res.Score == 0 {
		t.Error("a business with website signals should still score")
	}
	if !res.LowConfidence {
		t.Errorf("confidence %.2f should be low: ads and hiring were never checked", res.Confidence)
	}
	if !res.NeedsManualCheck {
		t.Error("ad status was never recorded; a manual check is needed")
	}
}

// Confidence is coverage: the share of available points we could actually
// check. Missing the 40-point ad signal has to show up as missing confidence.
func TestConfidenceReflectsCoverage(t *testing.T) {
	cfg := defaultConfig(t)

	nothing := cfg.Score(Input{Business: model.Business{ID: 1}})
	fullWebsite := cfg.Score(Input{
		Business: model.Business{ID: 2},
		Signals: []model.Signal{
			sig(model.TypeContactForm, "true"),
			sig(model.TypeContactEmail, "a@b.com"),
			sig(model.TypeResponseTimeHours, "24"),
			sig(model.TypeSizeBand, "smb"),
		},
	})
	everything := cfg.Score(Input{
		Business: model.Business{ID: 3},
		Signals: []model.Signal{
			sig(model.TypeContactForm, "true"),
			sig(model.TypeContactEmail, "a@b.com"),
			sig(model.TypeResponseTimeHours, "24"),
			sig(model.TypeSizeBand, "smb"),
			sig(model.TypeRunningAds, "true"),
			sig(model.TypeHiringLeadRole, "false"),
		},
	})

	if !(nothing.Confidence < fullWebsite.Confidence && fullWebsite.Confidence < everything.Confidence) {
		t.Errorf("confidence should rise with coverage: %.2f, %.2f, %.2f",
			nothing.Confidence, fullWebsite.Confidence, everything.Confidence)
	}
	if everything.Confidence != 1.0 {
		t.Errorf("full coverage = %.2f, want 1.0", everything.Confidence)
	}
}

// The explanation must say what it does not know, or a provisional score reads
// as a certain one.
func TestUncheckedSignalsAreNamed(t *testing.T) {
	cfg := defaultConfig(t)

	res := cfg.Score(Input{
		Business: model.Business{ID: 1, Name: "Partial"},
		Signals:  []model.Signal{sig(model.TypeContactForm, "true")},
	})

	if len(res.Unchecked) == 0 {
		t.Fatal("nothing reported as unchecked despite missing ad and hiring data")
	}
	found := false
	for _, u := range res.Unchecked {
		if u == string(model.TypeRunningAds) {
			found = true
		}
	}
	if !found {
		t.Errorf("Unchecked = %v, want running_ads named", res.Unchecked)
	}
	if !strings.Contains(res.Explanation, "Not yet checked") {
		t.Errorf("explanation does not admit the gap: %q", res.Explanation)
	}
	if !strings.Contains(res.Explanation, "whether they run ads") {
		t.Errorf("explanation should name the gap in plain English: %q", res.Explanation)
	}
}

// A review count stands in for a size band when none was recorded by hand.
func TestSizeBandInferredFromReviewCount(t *testing.T) {
	cfg := defaultConfig(t)

	tests := []struct {
		reviews   int
		wantScore bool
	}{
		{2, false},   // micro: too small or too new
		{40, true},   // smb: the target
		{300, false}, // mid
		{5000, false},
	}
	for _, tc := range tests {
		reviews := tc.reviews
		res := cfg.Score(Input{
			Business: model.Business{ID: 1, ReviewCount: &reviews},
		})
		_, scored := componentPoints(res)["target_size"]
		if scored != tc.wantScore {
			t.Errorf("%d reviews scored=%v, want %v", tc.reviews, scored, tc.wantScore)
		}
	}
}

// A hand-recorded band beats an inference from review volume.
func TestManualSizeBandWinsOverReviewCount(t *testing.T) {
	cfg := defaultConfig(t)
	reviews := 5000 // would infer "large"

	res := cfg.Score(Input{
		Business: model.Business{ID: 1, ReviewCount: &reviews},
		Signals:  []model.Signal{{Type: model.TypeSizeBand, Value: "smb", Source: model.SourceManual, Confidence: 1}},
	})
	if _, scored := componentPoints(res)["target_size"]; !scored {
		t.Error("a hand-recorded size band must take precedence over the review-count inference")
	}
}

// The breakdown should lead with the reason that moved the number.
func TestComponentsSortedByImpact(t *testing.T) {
	cfg := defaultConfig(t)

	res := cfg.Score(Input{
		Business: model.Business{ID: 1},
		Signals: []model.Signal{
			sig(model.TypeContactEmail, "a@b.com"),
			sig(model.TypeRunningAds, "true"),
			sig(model.TypeContactForm, "true"),
		},
	})
	if len(res.Components) < 3 {
		t.Fatalf("got %d components, want at least 3", len(res.Components))
	}
	for i := 1; i < len(res.Components); i++ {
		if abs(res.Components[i-1].Points) < abs(res.Components[i].Points) {
			t.Errorf("components not ordered by impact: %v", res.Components)
		}
	}
	if res.Components[0].Rule != "running_ads" {
		t.Errorf("first component = %q, want running_ads", res.Components[0].Rule)
	}
}

// Evidence is what makes a component quotable in a cold email.
func TestComponentsCarryEvidence(t *testing.T) {
	cfg := defaultConfig(t)

	res := cfg.Score(Input{
		Business: model.Business{ID: 1, Name: "Acme"},
		Signals: []model.Signal{
			sigWithDetail(model.TypeResponseTimeHours, "24", `{"quote":"We respond within 24 hours."}`),
			sig(model.TypeContactEmail, "hello@acme.com"),
		},
	})

	for _, c := range res.Components {
		switch c.Rule {
		case "slow_stated_response":
			if !strings.Contains(c.Evidence, "24 hours") {
				t.Errorf("response evidence = %q, want the quoted sentence", c.Evidence)
			}
		case "public_email":
			if c.Evidence != "hello@acme.com" {
				t.Errorf("email evidence = %q", c.Evidence)
			}
		}
	}
}

// The explanation is the deliverable: it must name the business, the score,
// and the reasons, in sendable English.
func TestExplanationReadsAsProse(t *testing.T) {
	cfg := defaultConfig(t)

	res := cfg.Score(Input{
		Business: model.Business{ID: 1, Name: "Acme Recruiting"},
		Signals: []model.Signal{
			sig(model.TypeRunningAds, "true"),
			sig(model.TypeContactForm, "true"),
			sig(model.TypeContactEmail, "hello@acme.com"),
			sig(model.TypeSizeBand, "smb"),
			sig(model.TypeHiringLeadRole, "false"),
			sigWithDetail(model.TypeResponseTimeHours, "24", `{"quote":"We respond within 24 hours."}`),
		},
	})

	for _, want := range []string{
		"Acme Recruiting",
		"is currently running paid ads",
		"has a contact form with no CRM or scheduling tool behind it",
	} {
		if !strings.Contains(res.Explanation, want) {
			t.Errorf("explanation missing %q:\n%s", want, res.Explanation)
		}
	}
	if strings.Contains(res.Explanation, "_") {
		t.Errorf("explanation leaks an identifier rather than prose:\n%s", res.Explanation)
	}
	t.Logf("explanation: %s", res.Explanation)
}

func TestExplanationHandlesNegativesSeparately(t *testing.T) {
	cfg := defaultConfig(t)

	res := cfg.Score(Input{
		Business: model.Business{ID: 1, Name: "Globex"},
		Signals: []model.Signal{
			sig(model.TypeContactForm, "true"),
			sig(model.TypeEnterpriseMarker, "salesforce"),
		},
	})
	if !strings.Contains(res.Explanation, "Against that") {
		t.Errorf("penalties should be introduced as a counterpoint:\n%s", res.Explanation)
	}
}

func TestExplanationWithNoSignals(t *testing.T) {
	cfg := defaultConfig(t)
	res := cfg.Score(Input{Business: model.Business{ID: 1, Name: "Unknown Co"}})

	if !strings.Contains(res.Explanation, "No scoring signals matched") {
		t.Errorf("explanation should say plainly that nothing matched:\n%s", res.Explanation)
	}
	if res.Score != 0 {
		t.Errorf("Score = %d, want 0", res.Score)
	}
}

// brief needs a sendable opening line, drawn from the strongest matched rule.
func TestObservationComesFromTheStrongestRule(t *testing.T) {
	cfg := defaultConfig(t)

	res := cfg.Score(Input{
		Business: model.Business{ID: 1, Name: "Acme"},
		Signals: []model.Signal{
			sig(model.TypeRunningAds, "true"),
			sig(model.TypeContactForm, "true"),
		},
	})
	if res.Observation == "" {
		t.Fatal("no observation produced")
	}
	if !strings.Contains(res.Observation, "running ads") {
		t.Errorf("observation = %q, want it drawn from the 40-point ad rule", res.Observation)
	}

	// With no ads, the next-strongest rule that offers one takes over.
	noAds := cfg.Score(Input{
		Business: model.Business{ID: 2, Name: "Acme"},
		Signals:  []model.Signal{sig(model.TypeContactForm, "true")},
	})
	if !strings.Contains(noAds.Observation, "contact form") {
		t.Errorf("observation = %q, want the contact-form opener", noAds.Observation)
	}
}

// A penalty must never supply the opening line of a sales email.
func TestObservationIgnoresPenalties(t *testing.T) {
	cfg := defaultConfig(t)
	res := cfg.Score(Input{
		Business: model.Business{ID: 1, Name: "Globex"},
		Signals:  []model.Signal{sig(model.TypeEnterpriseMarker, "salesforce")},
	})
	if res.Observation != "" {
		t.Errorf("Observation = %q, want none: penalties do not open emails", res.Observation)
	}
}

func TestNeedsManualCheck(t *testing.T) {
	cfg := defaultConfig(t)

	without := cfg.Score(Input{Business: model.Business{ID: 1}})
	if !without.NeedsManualCheck {
		t.Error("a business with no ad signal must be flagged for a manual check")
	}

	// Either value clears the flag: what matters is that someone looked.
	for _, value := range []string{"true", "false"} {
		res := cfg.Score(Input{
			Business: model.Business{ID: 1},
			Signals:  []model.Signal{{Type: model.TypeRunningAds, Value: value, Source: model.SourceManual, Confidence: 1}},
		})
		if res.NeedsManualCheck {
			t.Errorf("running_ads=%s should clear the manual-check flag", value)
		}
	}
}

// Retuning weights must not silently move what --min-score selects, which is
// why the score is normalized rather than raw.
func TestNormalizationSurvivesRetuning(t *testing.T) {
	doubled := strings.NewReplacer(
		"points: 40", "points: 80",
		"points: 20", "points: 40",
		"points: 15", "points: 30",
		"points: 10", "points: 20",
	).Replace(string(DefaultYAML()))

	retuned, err := Parse([]byte(doubled))
	if err != nil {
		t.Fatalf("Parse retuned config: %v", err)
	}

	signals := []model.Signal{
		sig(model.TypeRunningAds, "true"),
		sig(model.TypeContactForm, "true"),
	}
	in := Input{Business: model.Business{ID: 1, Name: "Acme"}, Signals: signals}

	base := defaultConfig(t).Score(in)
	scaled := retuned.Score(in)

	if base.Score != scaled.Score {
		t.Errorf("doubling every weight changed the score from %d to %d; normalization is not holding",
			base.Score, scaled.Score)
	}
	if scaled.Raw == base.Raw {
		t.Error("the raw sum should have changed even though the normalized score did not")
	}
}

// The repo's example file is what operators copy; it must not drift from the
// built-in defaults.
func TestExampleWeightsMatchDefaults(t *testing.T) {
	example, err := os.ReadFile("../../weights.example.yaml")
	if err != nil {
		t.Fatalf("read weights.example.yaml: %v", err)
	}
	if string(example) != string(DefaultYAML()) {
		t.Error("weights.example.yaml has drifted from the built-in defaults; copy internal/scoring/weights.default.yaml over it")
	}
}
