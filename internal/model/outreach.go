package model

import (
	"fmt"
	"strings"
	"time"
)

// OutreachStatus is the pipeline position of a prospect.
type OutreachStatus string

const (
	StatusNotContacted OutreachStatus = "not_contacted"
	StatusEmailed1     OutreachStatus = "emailed_1"
	StatusEmailed2     OutreachStatus = "emailed_2"
	StatusEmailed3     OutreachStatus = "emailed_3"
	StatusReplied      OutreachStatus = "replied"
	StatusCallBooked   OutreachStatus = "call_booked"
	StatusWon          OutreachStatus = "won"
	StatusDead         OutreachStatus = "dead"
)

var outreachStatuses = []OutreachStatus{
	StatusNotContacted, StatusEmailed1, StatusEmailed2, StatusEmailed3,
	StatusReplied, StatusCallBooked, StatusWon, StatusDead,
}

// LookupOutreachStatus validates `prospect status --set` input.
func LookupOutreachStatus(s string) (OutreachStatus, error) {
	want := OutreachStatus(strings.TrimSpace(s))
	for _, st := range outreachStatuses {
		if st == want {
			return st, nil
		}
	}
	return "", fmt.Errorf("unknown status %q; known statuses: %s",
		s, strings.Join(OutreachStatusNames(), ", "))
}

// OutreachStatusNames returns the statuses in pipeline order for help text.
func OutreachStatusNames() []string {
	names := make([]string, len(outreachStatuses))
	for i, s := range outreachStatuses {
		names[i] = string(s)
	}
	return names
}

// Outreach is the current state of one business.
type Outreach struct {
	BusinessID int64
	Status     OutreachStatus
	Notes      string
	UpdatedAt  time.Time
}

// OutreachEvent is one transition, kept so a sequence of touches is
// reconstructable rather than overwritten.
type OutreachEvent struct {
	ID         int64
	BusinessID int64
	From       OutreachStatus
	To         OutreachStatus
	Note       string
	At         time.Time
}
