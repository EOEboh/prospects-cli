package model

import (
	"strings"
	"testing"
	"time"
)

// The signal vocabulary is a closed set so a typo fails at entry rather than
// becoming a silently missing 40-point signal.
func TestLookupSignalType(t *testing.T) {
	tests := []struct {
		in      string
		wantErr bool
	}{
		{"running_ads", false},
		{"contact_form", false},
		{"automation_tag", false},
		{"  running_ads  ", false}, // padding is tolerated
		{"running_add", true},      // a plausible typo
		{"RUNNING_ADS", true},      // types are lower-case by convention
		{"", true},
		{"made_up", true},
	}
	for _, tc := range tests {
		_, err := LookupSignalType(tc.in)
		if tc.wantErr && err == nil {
			t.Errorf("LookupSignalType(%q) should have failed", tc.in)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("LookupSignalType(%q): %v", tc.in, err)
		}
	}
}

// The error has to tell the operator what to write instead.
func TestUnknownSignalTypeErrorListsTheVocabulary(t *testing.T) {
	_, err := LookupSignalType("running_add")
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"running_ads", "contact_form", "size_band"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should list %q: %v", want, err)
		}
	}
}

// Whether a type is multi-valued decides if a new observation supersedes the
// old one or coexists with it, so the flags matter.
func TestSignalMultiValuedFlags(t *testing.T) {
	multi := map[SignalType]bool{
		// A business can run HubSpot and Calendly at once.
		TypeAutomationTag: true,
		// And can show several enterprise markers.
		TypeEnterpriseMarker: true,
		// These are single facts that replace their predecessor.
		TypeRunningAds:        false,
		TypeContactForm:       false,
		TypeChatWidget:        false,
		TypeResponseTimeHours: false,
		TypeContactEmail:      false,
		TypeHiringLeadRole:    false,
		TypeSizeBand:          false,
		TypeRobotsDisallowed:  false,
	}

	for typ, wantMulti := range multi {
		spec, err := LookupSignalType(string(typ))
		if err != nil {
			t.Fatalf("LookupSignalType(%q): %v", typ, err)
		}
		if spec.Multi != wantMulti {
			t.Errorf("%s Multi = %v, want %v", typ, spec.Multi, wantMulti)
		}
	}
}

// Every type needs help text: `prospect signal --help` is the only place the
// operator sees the vocabulary.
func TestEverySignalTypeIsDocumented(t *testing.T) {
	specs := SignalSpecs()
	if len(specs) != len(SignalTypeNames()) {
		t.Fatalf("%d specs but %d names", len(specs), len(SignalTypeNames()))
	}
	for _, s := range specs {
		if strings.TrimSpace(s.Desc) == "" {
			t.Errorf("signal type %q has no description", s.Type)
		}
		if s.Type == "" {
			t.Error("a spec has an empty type")
		}
	}
}

// The ones normally entered by hand are flagged as such, so help can mark them.
func TestManualSignalTypes(t *testing.T) {
	manual := map[SignalType]bool{
		TypeRunningAds:     true,
		TypeHiringLeadRole: true,
		TypeSizeBand:       true,
	}
	for typ, want := range manual {
		spec, err := LookupSignalType(string(typ))
		if err != nil {
			t.Fatalf("LookupSignalType(%q): %v", typ, err)
		}
		if spec.Manual != want {
			t.Errorf("%s Manual = %v, want %v", typ, spec.Manual, want)
		}
	}
}

func TestSignalTypeNamesAreSorted(t *testing.T) {
	names := SignalTypeNames()
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Errorf("SignalTypeNames not sorted: %q before %q", names[i-1], names[i])
		}
	}
}

func TestLookupOutreachStatus(t *testing.T) {
	tests := []struct {
		in      string
		wantErr bool
	}{
		{"not_contacted", false},
		{"emailed_1", false},
		{"won", false},
		{"dead", false},
		{"  replied  ", false},
		{"emailed_9", true},
		{"", true},
		{"Won", true},
	}
	for _, tc := range tests {
		_, err := LookupOutreachStatus(tc.in)
		if tc.wantErr && err == nil {
			t.Errorf("LookupOutreachStatus(%q) should have failed", tc.in)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("LookupOutreachStatus(%q): %v", tc.in, err)
		}
	}
}

// Statuses are listed in pipeline order, because that is how help text reads.
func TestOutreachStatusNamesAreInPipelineOrder(t *testing.T) {
	want := []string{
		"not_contacted", "emailed_1", "emailed_2", "emailed_3",
		"replied", "call_booked", "won", "dead",
	}
	got := OutreachStatusNames()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSignalIsCurrent(t *testing.T) {
	live := Signal{}
	if !live.IsCurrent() {
		t.Error("a signal with no superseded_at is current")
	}

	now := time.Now()
	old := Signal{SupersededAt: &now}
	if old.IsCurrent() {
		t.Error("a superseded signal is not current")
	}
}
