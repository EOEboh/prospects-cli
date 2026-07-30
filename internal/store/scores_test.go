package store

import (
	"context"
	"testing"

	"github.com/EOEboh/prospects-cli/internal/model"
)

// scoreFixture stores a score for a business, returning its id.
func scoreFixture(t *testing.T, db *DB, runID int64, b model.Business, score int, needsAdCheck bool) int64 {
	t.Helper()
	ctx := context.Background()

	id, _ := seedBusiness(t, db, b)
	s := &model.Score{
		BusinessID:       id,
		RunID:            runID,
		Score:            score,
		Raw:              score,
		MaxPossible:      110,
		Confidence:       0.8,
		NeedsManualCheck: needsAdCheck,
		Breakdown: []model.Component{
			{Rule: "running_ads", Label: "Currently running ads", Points: 40, Evidence: "checked ad library"},
		},
		Explanation: b.Name + " scores well.",
		WeightsHash: "abc123",
	}
	if err := db.SaveScore(ctx, s); err != nil {
		t.Fatalf("SaveScore: %v", err)
	}
	return id
}

func newRun(t *testing.T, db *DB) int64 {
	t.Helper()
	runID, err := db.StartRun(context.Background(), "score", nil)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	return runID
}

func TestListScoredRanksByScore(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	runID := newRun(t, db)

	scoreFixture(t, db, runID, model.Business{Name: "Low", Website: "https://low.com"}, 20, false)
	scoreFixture(t, db, runID, model.Business{Name: "High", Website: "https://high.com"}, 90, false)
	scoreFixture(t, db, runID, model.Business{Name: "Mid", Website: "https://mid.com"}, 55, false)

	rows, err := db.ListScored(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("ListScored: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("%d rows, want 3", len(rows))
	}
	if rows[0].Business.Name != "High" || rows[2].Business.Name != "Low" {
		t.Errorf("order = %s, %s, %s; want High, Mid, Low",
			rows[0].Business.Name, rows[1].Business.Name, rows[2].Business.Name)
	}
	// The breakdown must survive the round trip: it is what makes a row useful.
	if len(rows[0].Score.Breakdown) != 1 || rows[0].Score.Breakdown[0].Rule != "running_ads" {
		t.Errorf("breakdown lost in storage: %+v", rows[0].Score.Breakdown)
	}
}

func TestListScoredFilters(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	runID := newRun(t, db)

	high := scoreFixture(t, db, runID, model.Business{Name: "High", Website: "https://high.com"}, 90, true)
	scoreFixture(t, db, runID, model.Business{Name: "Low", Website: "https://low.com"}, 20, false)
	contacted := scoreFixture(t, db, runID, model.Business{Name: "Contacted", Website: "https://c.com"}, 70, false)

	if _, err := db.SetOutreachStatus(ctx, contacted, model.StatusEmailed1, ""); err != nil {
		t.Fatalf("SetOutreachStatus: %v", err)
	}

	tests := []struct {
		name   string
		filter ListFilter
		want   []string
	}{
		{"min score", ListFilter{MinScore: 60}, []string{"High", "Contacted"}},
		{"max score", ListFilter{MaxScore: 50}, []string{"Low"}},
		{"needs ad check", ListFilter{NeedsAdCheck: true}, []string{"High"}},
		{"by status", ListFilter{Status: model.StatusEmailed1}, []string{"Contacted"}},
		{"uncontacted only", ListFilter{UncontactedOnly: true}, []string{"High", "Low"}},
		{"limit", ListFilter{Limit: 1}, []string{"High"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := db.ListScored(ctx, tc.filter)
			if err != nil {
				t.Fatalf("ListScored: %v", err)
			}
			got := make([]string, len(rows))
			for i, r := range rows {
				got[i] = r.Business.Name
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("got %v, want %v", got, tc.want)
					break
				}
			}
		})
	}

	_ = high
}

// Suppression is enforced by the view, so every listing inherits it.
func TestListScoredExcludesSuppressed(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	runID := newRun(t, db)

	keep := scoreFixture(t, db, runID, model.Business{Name: "Keep", Website: "https://keep.com"}, 80, false)
	drop := scoreFixture(t, db, runID, model.Business{Name: "Drop", Website: "https://drop.com"}, 95, false)

	if _, err := db.Suppress(ctx, drop, "asked to be removed", false); err != nil {
		t.Fatalf("Suppress: %v", err)
	}

	rows, err := db.ListScored(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("ListScored: %v", err)
	}
	if len(rows) != 1 || rows[0].Business.ID != keep {
		t.Errorf("suppressed business appeared in the listing: %+v", rows)
	}
}

// Only the newest score per business is listed, so a rescore does not
// duplicate rows.
func TestListScoredUsesTheLatestScoreOnly(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	firstRun := newRun(t, db)
	id := scoreFixture(t, db, firstRun, model.Business{Name: "Acme", Website: "https://acme.com"}, 40, true)

	// A later run scores the same business higher, as recording the ad signal
	// would.
	secondRun := newRun(t, db)
	if err := db.SaveScore(ctx, &model.Score{
		BusinessID: id, RunID: secondRun, Score: 77, Raw: 85, MaxPossible: 110,
		Confidence: 0.9, Breakdown: []model.Component{}, Explanation: "now with ads",
		WeightsHash: "abc123",
	}); err != nil {
		t.Fatalf("SaveScore: %v", err)
	}

	rows, err := db.ListScored(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("ListScored: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("%d rows, want 1: only the latest score should list", len(rows))
	}
	if rows[0].Score.Score != 77 {
		t.Errorf("score = %d, want the latest (77)", rows[0].Score.Score)
	}
	if rows[0].Score.NeedsManualCheck {
		t.Error("the stale needs-check flag leaked from the earlier score")
	}
}

// A business with no score at all should not appear: it has not been assessed.
func TestListScoredSkipsUnscoredBusinesses(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	seedBusiness(t, db, model.Business{Name: "Never Scored", Website: "https://ns.com"})

	rows, err := db.ListScored(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("ListScored: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("%d rows, want 0", len(rows))
	}
}

// Scoring covers every active business, including ones with no signals: a zero
// is a legitimate answer and hides no work.
func TestBusinessesToScoreIncludesUnenriched(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	seedBusiness(t, db, model.Business{Name: "Enriched", Website: "https://e.com"})
	seedBusiness(t, db, model.Business{Name: "Bare", City: "Austin"})

	businesses, err := db.BusinessesToScore(ctx, 0)
	if err != nil {
		t.Fatalf("BusinessesToScore: %v", err)
	}
	if len(businesses) != 2 {
		t.Errorf("%d businesses, want 2", len(businesses))
	}
}
