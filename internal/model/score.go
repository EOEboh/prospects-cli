package model

import "time"

// Component is one rule's contribution to a score. The breakdown is the
// point of this tool: the reason a business scored 85 is the first line of
// the cold email, so every component carries its own evidence.
type Component struct {
	Rule     string `json:"rule"`
	Label    string `json:"label"`
	Points   int    `json:"points"`
	Evidence string `json:"evidence,omitempty"`
}

// Score is one business's evaluation under one weights config.
type Score struct {
	ID         int64
	BusinessID int64
	RunID      int64

	// Score is the normalized 0..100 value, which is what gets printed and
	// what --min-score compares against. Raw is the signed weighted sum and
	// MaxPossible is the sum of positive weights in the active config, so a
	// retuned weights.yaml does not silently move what --min-score 60 selects.
	Score        int
	Raw          int
	MaxPossible  int
	Confidence   float64 // source coverage, 0..1
	LowConfident bool    // derived; partial data still scores, but says so

	// NeedsManualCheck is set when a high-value signal — currently ads — has
	// never been recorded for this business. brief surfaces these.
	NeedsManualCheck bool

	Breakdown   []Component
	Explanation string

	// WeightsHash ties a score to the config that produced it, so scores from
	// before and after a retune are distinguishable.
	WeightsHash string
	CreatedAt   time.Time
}
