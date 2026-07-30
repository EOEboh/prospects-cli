package scoring

import (
	"fmt"
	"strings"

	"github.com/EOEboh/prospects-cli/internal/model"
)

// explain writes the plain-English reasoning behind a score.
//
// This is the output the whole tool exists for: the sentence that opens a cold
// email. It reads as observations about the business, with the evidence
// attached, and it says plainly what was not checked rather than implying the
// picture is complete.
func (c *Config) explain(b model.Business, res Result) string {
	name := strings.TrimSpace(b.Name)
	if name == "" {
		name = "This business"
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%s scores %d/100.", name, res.Score)

	positives, negatives := splitComponents(res.Components)

	switch {
	case len(positives) > 0:
		// "It" rather than the name again: the name is one clause back, and
		// repeating it reads like a form letter.
		sb.WriteString(" It ")
		sb.WriteString(joinClauses(clausesFor(positives, c)))
		sb.WriteByte('.')
	case len(negatives) == 0:
		sb.WriteString(" No scoring signals matched yet.")
	}

	if len(negatives) > 0 {
		sb.WriteString(" Against that, it ")
		sb.WriteString(joinClauses(clausesFor(negatives, c)))
		sb.WriteByte('.')
	}

	// Admitting the gap matters: a 64%-confident score presented as certain is
	// how a good prospect gets skipped or a bad one gets emailed.
	if len(res.Unchecked) > 0 {
		fmt.Fprintf(&sb, " Not yet checked: %s.", humanList(prettySignalNames(res.Unchecked)))
	}
	if res.LowConfidence {
		fmt.Fprintf(&sb, " Confidence is low (%.0f%% of available signals checked), so treat this as provisional.",
			res.Confidence*100)
	}

	return sb.String()
}

// clausesFor renders each component as "<explain> (<evidence>)".
func clausesFor(components []model.Component, c *Config) []string {
	byID := make(map[string]Rule, len(c.Rules))
	for _, r := range c.Rules {
		byID[r.ID] = r
	}

	out := make([]string, 0, len(components))
	for _, comp := range components {
		rule, ok := byID[comp.Rule]
		if !ok {
			continue
		}
		clause := strings.TrimSpace(rule.Explain)
		if comp.Evidence != "" {
			clause += " (" + trimEvidence(comp.Evidence) + ")"
		}
		out = append(out, clause)
	}
	return out
}

func splitComponents(components []model.Component) (positives, negatives []model.Component) {
	for _, comp := range components {
		if comp.Points >= 0 {
			positives = append(positives, comp)
		} else {
			negatives = append(negatives, comp)
		}
	}
	return positives, negatives
}

// joinClauses builds "a, b and c" from clauses that already read as verb
// phrases sharing one subject.
func joinClauses(clauses []string) string {
	switch len(clauses) {
	case 0:
		return ""
	case 1:
		return clauses[0]
	case 2:
		return clauses[0] + ", and " + clauses[1]
	default:
		return strings.Join(clauses[:len(clauses)-1], ", ") + ", and " + clauses[len(clauses)-1]
	}
}

// humanList joins plain nouns: "ads, hiring and size".
func humanList(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	default:
		return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
	}
}

// prettySignalNames turns signal identifiers into something readable in a
// sentence, since the explanation is written for a person.
var signalPhrases = map[string]string{
	string(model.TypeRunningAds):        "whether they run ads",
	string(model.TypeHiringLeadRole):    "whether they are hiring for lead handling",
	string(model.TypeSizeBand):          "company size",
	string(model.TypeContactForm):       "their contact form",
	string(model.TypeContactEmail):      "a public email address",
	string(model.TypeResponseTimeHours): "a stated response time",
	string(model.TypeChatWidget):        "live chat",
	string(model.TypeAutomationTag):     "marketing tooling",
	string(model.TypeEnterpriseMarker):  "enterprise tooling",
}

func prettySignalNames(types []string) []string {
	out := make([]string, 0, len(types))
	for _, t := range types {
		if phrase, ok := signalPhrases[t]; ok {
			out = append(out, phrase)
			continue
		}
		out = append(out, strings.ReplaceAll(t, "_", " "))
	}
	return out
}

// trimEvidence keeps a quoted sentence readable inside a longer clause.
const maxEvidenceLen = 90

func trimEvidence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= maxEvidenceLen {
		return s
	}
	cut := s[:maxEvidenceLen]
	if i := strings.LastIndex(cut, " "); i > maxEvidenceLen/2 {
		cut = cut[:i]
	}
	return cut + "…"
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	// Business names are already capitalized; this only rescues a lowercase
	// fallback like "this business".
	r := []rune(s)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] = r[0] - 'a' + 'A'
	}
	return string(r)
}
