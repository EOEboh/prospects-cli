package model

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// SignalSource identifies who observed a fact. Manual entries live in the
// same table as automated ones: the ad signal is primarily hand-gathered, and
// scoring must not care where a fact came from.
type SignalSource string

const (
	SourceCSV     SignalSource = "csv"
	SourceWebsite SignalSource = "website"
	SourcePlaces  SignalSource = "places"
	SourceMetaAds SignalSource = "meta_ads"
	SourceManual  SignalSource = "manual"
)

// SignalType is the closed set of facts the scoring engine understands.
type SignalType string

const (
	// TypeRunningAds is the dominant buying signal. Usually recorded by hand
	// from the Ad Library web UI; the API source is optional and off by default.
	TypeRunningAds SignalType = "running_ads"

	TypeContactForm       SignalType = "contact_form"
	TypeChatWidget        SignalType = "chat_widget"
	TypeAutomationTag     SignalType = "automation_tag"
	TypeEnterpriseMarker  SignalType = "enterprise_marker"
	TypeResponseTimeHours SignalType = "response_time_hours"
	TypeContactEmail      SignalType = "contact_email"
	TypeHiringLeadRole    SignalType = "hiring_lead_role"
	TypeSizeBand          SignalType = "size_band"

	// TypeRobotsDisallowed records that a fetch was skipped and why, so an
	// unenriched business is distinguishable from an unfetchable one.
	TypeRobotsDisallowed SignalType = "robots_disallowed"
)

// SizeBand values for TypeSizeBand.
const (
	SizeMicro = "micro" // roughly 1
	SizeSMB   = "smb"   // roughly 2-50, the target range
	SizeMid   = "mid"
	SizeLarge = "large"
)

// SignalSpec describes how a type behaves when a new observation arrives.
type SignalSpec struct {
	Type SignalType

	// Multi means several values coexist as current facts. A business can run
	// HubSpot and Calendly at once, so a new automation_tag must not supersede
	// an existing one. Single-valued types supersede on change.
	Multi bool

	// Manual marks types normally entered by hand via `prospect signal`.
	Manual bool

	// Desc is shown in `prospect signal --help`, which is the only place the
	// operator sees the vocabulary.
	Desc string
}

var signalSpecs = map[SignalType]SignalSpec{
	TypeRunningAds:        {TypeRunningAds, false, true, "true|false — business is currently running paid ads"},
	TypeContactForm:       {TypeContactForm, false, false, "true|false — contact form present; detail holds the action endpoint"},
	TypeChatWidget:        {TypeChatWidget, false, false, "true|false — live chat widget detected"},
	TypeAutomationTag:     {TypeAutomationTag, true, false, "hubspot|calendly|intercom|mailchimp|typeform|… — one per detected tool"},
	TypeEnterpriseMarker:  {TypeEnterpriseMarker, true, false, "salesforce|… — indicator the business is too large to sell to"},
	TypeResponseTimeHours: {TypeResponseTimeHours, false, false, "integer hours from a stated response-time promise"},
	TypeContactEmail:      {TypeContactEmail, false, false, "a publicly listed business email address"},
	TypeHiringLeadRole:    {TypeHiringLeadRole, false, true, "true|false — hiring for a data-entry or lead-management role"},
	TypeSizeBand:          {TypeSizeBand, false, true, "micro|smb|mid|large — smb is the 2-50 employee target"},
	TypeRobotsDisallowed:  {TypeRobotsDisallowed, false, false, "true — robots.txt disallowed the fetch; detail holds the rule"},
}

// LookupSignalType validates operator input against the closed vocabulary.
func LookupSignalType(s string) (SignalSpec, error) {
	spec, ok := signalSpecs[SignalType(strings.TrimSpace(s))]
	if !ok {
		return SignalSpec{}, fmt.Errorf("unknown signal type %q; known types: %s",
			s, strings.Join(SignalTypeNames(), ", "))
	}
	return spec, nil
}

// SignalTypeNames returns every known type, sorted, for help text and errors.
func SignalTypeNames() []string {
	names := make([]string, 0, len(signalSpecs))
	for t := range signalSpecs {
		names = append(names, string(t))
	}
	sort.Strings(names)
	return names
}

// SignalSpecs returns every spec sorted by type, for generated help output.
func SignalSpecs() []SignalSpec {
	specs := make([]SignalSpec, 0, len(signalSpecs))
	for _, s := range signalSpecs {
		specs = append(specs, s)
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Type < specs[j].Type })
	return specs
}

// Signal is one observed fact about one business.
//
// Rows are appended when a value changes, not on every observation. Re-seeing
// the same value bumps LastSeenAt; a different value stamps SupersededAt on
// the old row and inserts a new one. That keeps the transition history —
// "started running ads" is the hot buying moment — without a duplicate row
// per weekly re-enrichment.
type Signal struct {
	ID         int64
	BusinessID int64
	Source     SignalSource
	Type       SignalType
	Value      string

	// Confidence is 0..1. Manual entries are 1.0; heuristic extraction from
	// page text is lower.
	Confidence float64

	// Detail is JSON evidence: the form action, the matched sentence, the tag
	// that fired. It is what makes an explanation quotable in a cold email.
	Detail string
	Note   string

	ObservedAt   time.Time  // first time this value was seen
	LastSeenAt   time.Time  // most recent confirmation
	SupersededAt *time.Time // non-nil once a differing value replaced it
	RunID        *int64
}

// IsCurrent reports whether the signal reflects present belief.
func (s Signal) IsCurrent() bool { return s.SupersededAt == nil }
