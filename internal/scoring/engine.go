package scoring

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/EOEboh/prospects-cli/internal/model"
)

// Input is everything the engine needs about one business.
type Input struct {
	Business model.Business
	Signals  []model.Signal // current signals only
}

// Result is a score with its reasoning.
type Result struct {
	Score            int
	Raw              int
	MaxPossible      int
	Confidence       float64
	LowConfidence    bool
	NeedsManualCheck bool
	Components       []model.Component
	Explanation      string

	// Observation is the suggested opening line for a cold email, drawn from
	// the highest-scoring rule that offers one.
	Observation string

	// Unchecked names the signals that could not be evaluated, so the
	// explanation can admit what it does not know.
	Unchecked []string
}

// Score evaluates one business against the configured rules.
//
// A business with partial data still scores: an unevaluable rule contributes
// nothing and lowers confidence rather than blocking the score. Refusing to
// score an incompletely-known business would hide most of the pipeline.
func (c *Config) Score(in Input) Result {
	signals := newSignalSet(in.Signals)

	// A review count stands in for a size band when none was recorded by
	// hand, so Places data feeds the size rule without a manual step.
	if !signals.has(model.TypeSizeBand) && in.Business.ReviewCount != nil {
		signals.add(model.Signal{
			BusinessID: in.Business.ID,
			Source:     model.SourcePlaces,
			Type:       model.TypeSizeBand,
			Value:      c.BandFor(*in.Business.ReviewCount),
			Confidence: 0.6, // inferred from review volume, not observed
			Detail: detailJSON(map[string]any{
				"inferred_from_reviews": *in.Business.ReviewCount,
			}),
		})
	}

	res := Result{MaxPossible: c.MaxPossible()}

	var checkable int
	for _, rule := range c.Rules {
		matched, evidence := evaluate(rule.When, signals)

		if rule.Points > 0 && c.ruleIsCheckable(rule, signals) {
			checkable += rule.Points
		}
		if !matched {
			continue
		}

		res.Raw += rule.Points
		res.Components = append(res.Components, model.Component{
			Rule:     rule.ID,
			Label:    rule.Label,
			Points:   rule.Points,
			Evidence: evidence,
		})
	}

	// Largest contributions first: the breakdown should lead with the reason
	// that actually moved the number.
	sort.SliceStable(res.Components, func(i, j int) bool {
		return abs(res.Components[i].Points) > abs(res.Components[j].Points)
	})

	if res.MaxPossible > 0 {
		res.Confidence = float64(checkable) / float64(res.MaxPossible)
		normalized := float64(res.Raw) / float64(res.MaxPossible) * 100
		res.Score = clamp(int(math.Round(normalized)), 0, 100)
	}
	res.LowConfidence = res.Confidence < c.MinConfidence
	res.Unchecked = c.uncheckedSignals(signals)

	if c.ManualCheckSignal != "" {
		// Only a source trusted to settle the question clears the flag. An
		// enabled-but-unreliable source recording "no ads found" is not the
		// same as somebody having checked.
		res.NeedsManualCheck = !signals.hasFromSources(
			model.SignalType(c.ManualCheckSignal), c.ManualCheckClearedBy)
	}

	res.Explanation = c.explain(in.Business, res)
	res.Observation = c.observation(res)
	return res
}

// ruleIsCheckable reports whether there is enough data to evaluate a rule
// honestly.
//
// A rule counts as checkable when we hold a current signal for one of the types
// it inspects, or when it tests for absence — an absence test is answerable
// precisely because nothing is there. This is what makes "we never checked
// whether they run ads" show up as missing confidence rather than a silent
// zero.
func (c *Config) ruleIsCheckable(rule Rule, signals *signalSet) bool {
	if conditionTestsAbsence(rule.When) {
		return true
	}
	for _, t := range rule.signalTypes() {
		if signals.has(t) {
			return true
		}
	}
	return false
}

// conditionTestsAbsence reports whether a condition can be satisfied by the
// absence of data.
func conditionTestsAbsence(c Condition) bool {
	if c.Absent || c.Not != nil {
		return true
	}
	for _, sub := range c.All {
		if conditionTestsAbsence(sub) {
			return true
		}
	}
	for _, sub := range c.Any {
		if conditionTestsAbsence(sub) {
			return true
		}
	}
	return false
}

// uncheckedSignals names the signal types that positive rules depend on but
// which have no data, so the explanation can say what it does not know.
func (c *Config) uncheckedSignals(signals *signalSet) []string {
	seen := make(map[string]bool)
	var out []string
	for _, rule := range c.Rules {
		if rule.Points <= 0 || c.ruleIsCheckable(rule, signals) {
			continue
		}
		for _, t := range rule.signalTypes() {
			if !signals.has(t) && !seen[string(t)] {
				seen[string(t)] = true
				out = append(out, string(t))
			}
		}
	}
	sort.Strings(out)
	return out
}

// observation picks the opening line for a cold email: the highest-scoring
// matched rule that offers one.
func (c *Config) observation(res Result) string {
	return c.ObservationFor(res.Components)
}

// ObservationFor derives an opening line from a stored breakdown.
//
// It is recomputed rather than persisted so that rewording an observation in
// weights.yaml takes effect immediately, without rescoring everything. A
// penalty never supplies the opener: "you run enterprise software" is not how
// a sales email starts.
func (c *Config) ObservationFor(components []model.Component) string {
	byID := make(map[string]Rule, len(c.Rules))
	for _, r := range c.Rules {
		byID[r.ID] = r
	}

	best := ""
	bestPoints := 0
	for _, comp := range components {
		if comp.Points <= bestPoints {
			continue
		}
		if rule, ok := byID[comp.Rule]; ok && rule.Observation != "" {
			best, bestPoints = strings.TrimSpace(rule.Observation), comp.Points
		}
	}
	return best
}

// signalSet indexes a business's current signals by type.
type signalSet struct {
	byType map[model.SignalType][]model.Signal
}

func newSignalSet(signals []model.Signal) *signalSet {
	s := &signalSet{byType: make(map[model.SignalType][]model.Signal)}
	for _, sig := range signals {
		s.byType[sig.Type] = append(s.byType[sig.Type], sig)
	}
	return s
}

func (s *signalSet) add(sig model.Signal) {
	s.byType[sig.Type] = append(s.byType[sig.Type], sig)
}

func (s *signalSet) has(t model.SignalType) bool { return len(s.byType[t]) > 0 }

// hasFromSources reports whether any current signal of this type came from one
// of the named sources. An empty list accepts any source.
func (s *signalSet) hasFromSources(t model.SignalType, sources []string) bool {
	if len(sources) == 0 {
		return s.has(t)
	}
	for _, sig := range s.byType[t] {
		for _, want := range sources {
			if strings.EqualFold(string(sig.Source), want) {
				return true
			}
		}
	}
	return false
}

func (s *signalSet) get(t model.SignalType) []model.Signal { return s.byType[t] }

// evaluate tests a condition and returns the evidence that satisfied it.
func evaluate(c Condition, signals *signalSet) (bool, string) {
	switch {
	case len(c.All) > 0:
		var evidence []string
		for _, sub := range c.All {
			ok, ev := evaluate(sub, signals)
			if !ok {
				return false, ""
			}
			if ev != "" {
				evidence = append(evidence, ev)
			}
		}
		return true, strings.Join(evidence, "; ")

	case len(c.Any) > 0:
		for _, sub := range c.Any {
			if ok, ev := evaluate(sub, signals); ok {
				return true, ev
			}
		}
		return false, ""

	case c.Not != nil:
		ok, _ := evaluate(*c.Not, signals)
		return !ok, ""
	}

	return evaluateSignal(c, signals)
}

func evaluateSignal(c Condition, signals *signalSet) (bool, string) {
	t := model.SignalType(c.Signal)
	matches := signals.get(t)

	if c.Absent {
		return len(matches) == 0, ""
	}
	if len(matches) == 0 {
		return false, ""
	}
	if c.Present {
		return true, evidenceFor(matches[0])
	}

	for _, sig := range matches {
		if c.Equals != "" && !strings.EqualFold(sig.Value, c.Equals) {
			continue
		}
		if len(c.OneOf) > 0 && !containsFold(c.OneOf, sig.Value) {
			continue
		}
		if c.AtLeast != nil || c.AtMost != nil {
			n, err := strconv.Atoi(strings.TrimSpace(sig.Value))
			if err != nil {
				continue
			}
			if c.AtLeast != nil && n < *c.AtLeast {
				continue
			}
			if c.AtMost != nil && n > *c.AtMost {
				continue
			}
		}
		return true, evidenceFor(sig)
	}
	return false, ""
}

// evidenceFor renders the concrete observation behind a signal, which is what
// turns a component into something quotable.
func evidenceFor(s model.Signal) string {
	var detail map[string]any
	if s.Detail != "" {
		_ = json.Unmarshal([]byte(s.Detail), &detail)
	}

	// Prefer the most quotable field the signal carries.
	for _, key := range []string{"quote", "action", "evidence", "rule"} {
		if v, ok := detail[key].(string); ok && v != "" {
			return v
		}
	}
	if n, ok := detail["inferred_from_reviews"].(float64); ok {
		return strconv.Itoa(int(n)) + " reviews"
	}

	switch s.Type {
	case model.TypeContactEmail:
		return s.Value
	case model.TypeAutomationTag, model.TypeEnterpriseMarker:
		return s.Value
	case model.TypeResponseTimeHours:
		return s.Value + " hours"
	case model.TypeSizeBand:
		// The band is already stated by the rule's explain text; repeating it
		// as evidence reads as "(smb)" and helps nobody.
		return ""
	}

	if s.Note != "" {
		return s.Note
	}
	return ""
}

func containsFold(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}

func detailJSON(v map[string]any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
