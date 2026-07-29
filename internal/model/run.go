package model

import "time"

// RunStatus tracks a pipeline execution's outcome.
type RunStatus string

const (
	RunRunning   RunStatus = "running"
	RunCompleted RunStatus = "completed"
	RunFailed    RunStatus = "failed"
	RunAborted   RunStatus = "aborted"
)

// Run is one command execution, kept for resumability and debugging.
type Run struct {
	ID         int64
	Command    string
	Params     string // JSON, enough to reproduce the invocation
	Status     RunStatus
	StartedAt  time.Time
	FinishedAt *time.Time
	Error      string
	Stats      string // JSON counters
}

// ItemStatus is the per-unit checkpoint state.
type ItemStatus string

const (
	ItemPending ItemStatus = "pending"
	ItemDone    ItemStatus = "done"
	ItemFailed  ItemStatus = "failed"
	ItemSkipped ItemStatus = "skipped"
)

// RunItem checkpoints one unit of work so a run that dies at business 34 of
// 50 resumes at 34 instead of re-burning quota.
//
// Key is a business ID for enrich, or a page marker such as "page:2" for
// Places discovery.
type RunItem struct {
	RunID     int64
	Key       string
	Status    ItemStatus
	Error     string
	UpdatedAt time.Time
}

// Suppression permanently excludes a business from every list, export and
// brief. Also recorded by domain, so the same business rediscovered under a
// new ID stays excluded.
type Suppression struct {
	BusinessID int64
	Domain     string
	Reason     string
	CreatedAt  time.Time
}

// APICall is one billable (or cache-served) external call, counted locally.
// Google's budget alerts notify but do not stop usage, so the ceiling is
// enforced here.
type APICall struct {
	ID       int64
	Provider string
	SKU      string
	Endpoint string
	Billable bool
	Cached   bool
	RunID    *int64
	CalledAt time.Time
	Month    string // "YYYY-MM"
}
