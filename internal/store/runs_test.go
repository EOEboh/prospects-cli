package store

import (
	"context"
	"errors"
	"testing"

	"github.com/EOEboh/prospects-cli/internal/model"
)

func TestRunLifecycle(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	runID, err := db.StartRun(ctx, "seed", map[string]any{"csv": "businesses.csv"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	var status, params string
	if err := db.QueryRowContext(ctx,
		`SELECT status, params FROM runs WHERE id = ?`, runID).Scan(&status, &params); err != nil {
		t.Fatalf("read run: %v", err)
	}
	if status != string(model.RunRunning) {
		t.Errorf("status = %q, want running", status)
	}
	if params != `{"csv":"businesses.csv"}` {
		t.Errorf("params = %q — a run must record the invocation that produced it", params)
	}

	if err := db.FinishRun(ctx, runID, map[string]int{"inserted": 2}, nil); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	var stats, runErr string
	var finishedAt *string
	if err := db.QueryRowContext(ctx,
		`SELECT status, stats, error, finished_at FROM runs WHERE id = ?`, runID).
		Scan(&status, &stats, &runErr, &finishedAt); err != nil {
		t.Fatalf("read finished run: %v", err)
	}
	if status != string(model.RunCompleted) {
		t.Errorf("status = %q, want completed", status)
	}
	if stats != `{"inserted":2}` {
		t.Errorf("stats = %q", stats)
	}
	if runErr != "" {
		t.Errorf("error = %q, want empty", runErr)
	}
	if finishedAt == nil {
		t.Error("finished_at not set")
	}
}

func TestFinishRunRecordsFailure(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	runID, err := db.StartRun(ctx, "enrich", nil)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := db.FinishRun(ctx, runID, nil, errors.New("network unreachable")); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	var status, message string
	if err := db.QueryRowContext(ctx,
		`SELECT status, error FROM runs WHERE id = ?`, runID).Scan(&status, &message); err != nil {
		t.Fatalf("read run: %v", err)
	}
	if status != string(model.RunFailed) {
		t.Errorf("status = %q, want failed", status)
	}
	if message != "network unreachable" {
		t.Errorf("error = %q", message)
	}
}

// The resumability contract: a run that died leaves its completed items marked,
// and the next run skips them instead of re-burning quota.
func TestResumeSkipsCompletedItems(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	runID, err := db.StartRun(ctx, "enrich", nil)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	for i := 1; i <= 33; i++ {
		if err := db.MarkRunItem(ctx, runID, itemKey(i), model.ItemDone, nil); err != nil {
			t.Fatalf("mark item %d: %v", i, err)
		}
	}
	if err := db.MarkRunItem(ctx, runID, itemKey(34), model.ItemFailed, errors.New("timeout")); err != nil {
		t.Fatalf("mark failed item: %v", err)
	}
	// The run died here.
	if err := db.FinishRun(ctx, runID, nil, errors.New("interrupted")); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	resumed, err := db.LastIncompleteRun(ctx, "enrich")
	if err != nil {
		t.Fatalf("LastIncompleteRun: %v", err)
	}
	if resumed == nil {
		t.Fatal("no incomplete run found to resume")
	}
	if resumed.ID != runID {
		t.Errorf("resumed run %d, want %d", resumed.ID, runID)
	}

	done, err := db.CompletedItems(ctx, resumed.ID)
	if err != nil {
		t.Fatalf("CompletedItems: %v", err)
	}
	if len(done) != 33 {
		t.Errorf("%d completed items, want 33", len(done))
	}
	// Item 34 failed, so it must be retried rather than skipped.
	if done[itemKey(34)] {
		t.Error("a failed item was reported as complete")
	}
	if !done[itemKey(33)] {
		t.Error("item 33 completed but was not recorded")
	}
}

// Retrying an item overwrites its earlier failure rather than accumulating rows.
func TestMarkRunItemIsIdempotent(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	runID, err := db.StartRun(ctx, "enrich", nil)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	if err := db.MarkRunItem(ctx, runID, "business:7", model.ItemFailed, errors.New("timeout")); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	if err := db.MarkRunItem(ctx, runID, "business:7", model.ItemDone, nil); err != nil {
		t.Fatalf("mark done: %v", err)
	}

	if n := countRows(t, db, "run_items"); n != 1 {
		t.Errorf("%d run_items rows, want 1", n)
	}
	done, err := db.CompletedItems(ctx, runID)
	if err != nil {
		t.Fatalf("CompletedItems: %v", err)
	}
	if !done["business:7"] {
		t.Error("the retried item is not marked complete")
	}
}

func TestLastIncompleteRunIgnoresCompletedAndOtherCommands(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	completed, err := db.StartRun(ctx, "enrich", nil)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := db.FinishRun(ctx, completed, nil, nil); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	// An unfinished run of a different command must not be picked up.
	if _, err := db.StartRun(ctx, "seed", nil); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	got, err := db.LastIncompleteRun(ctx, "enrich")
	if err != nil {
		t.Fatalf("LastIncompleteRun: %v", err)
	}
	if got != nil {
		t.Errorf("found run %d, want none: the only enrich run completed cleanly", got.ID)
	}
}

func itemKey(i int) string {
	return "business:" + string(rune('0'+i/10)) + string(rune('0'+i%10))
}
