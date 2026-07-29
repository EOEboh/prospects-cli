package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/EOEboh/prospects-cli/internal/model"
)

// StartRun opens a run and returns its id. Params is stored as JSON so a run
// can be traced back to the invocation that produced it.
func (db *DB) StartRun(ctx context.Context, command string, params any) (int64, error) {
	encoded := "{}"
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return 0, fmt.Errorf("encode run params: %w", err)
		}
		encoded = string(b)
	}

	res, err := db.ExecContext(ctx,
		`INSERT INTO runs (command, params, status, started_at) VALUES (?, ?, ?, ?)`,
		command, encoded, model.RunRunning, FormatTime(Now()))
	if err != nil {
		return 0, fmt.Errorf("start run %s: %w", command, err)
	}
	return res.LastInsertId()
}

// FinishRun closes a run. A non-nil runErr marks it failed and records the
// message, so a partial run is distinguishable from a clean one when the next
// invocation decides what to resume.
func (db *DB) FinishRun(ctx context.Context, runID int64, stats any, runErr error) error {
	status := model.RunCompleted
	message := ""
	if runErr != nil {
		status = model.RunFailed
		message = runErr.Error()
	}

	encoded := "{}"
	if stats != nil {
		b, err := json.Marshal(stats)
		if err != nil {
			return fmt.Errorf("encode run stats: %w", err)
		}
		encoded = string(b)
	}

	_, err := db.ExecContext(ctx,
		`UPDATE runs SET status = ?, finished_at = ?, error = ?, stats = ? WHERE id = ?`,
		status, FormatTime(Now()), message, encoded, runID)
	if err != nil {
		return fmt.Errorf("finish run %d: %w", runID, err)
	}
	return nil
}

// MarkRunItem checkpoints one unit of work. Upserting means a retried item
// overwrites its earlier failure rather than accumulating rows.
func (db *DB) MarkRunItem(ctx context.Context, runID int64, key string, status model.ItemStatus, itemErr error) error {
	message := ""
	if itemErr != nil {
		message = itemErr.Error()
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO run_items (run_id, item_key, status, error, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(run_id, item_key) DO UPDATE SET
		   status = excluded.status, error = excluded.error, updated_at = excluded.updated_at`,
		runID, key, status, message, FormatTime(Now()))
	if err != nil {
		return fmt.Errorf("mark run item %s: %w", key, err)
	}
	return nil
}

// LastIncompleteRun finds the most recent run of a command that did not
// complete, which is what a resumed invocation continues from.
func (db *DB) LastIncompleteRun(ctx context.Context, command string) (*model.Run, error) {
	var (
		r          model.Run
		startedAt  string
		finishedAt sql.NullString
	)
	err := db.QueryRowContext(ctx,
		`SELECT id, command, params, status, started_at, finished_at, error, stats
		 FROM runs WHERE command = ? AND status IN (?, ?)
		 ORDER BY id DESC LIMIT 1`,
		command, model.RunRunning, model.RunFailed).
		Scan(&r.ID, &r.Command, &r.Params, &r.Status, &startedAt, &finishedAt, &r.Error, &r.Stats)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find last incomplete %s run: %w", command, err)
	}

	if r.StartedAt, err = ParseTime(startedAt); err != nil {
		return nil, err
	}
	if finishedAt.Valid {
		t, err := ParseTime(finishedAt.String)
		if err != nil {
			return nil, err
		}
		r.FinishedAt = &t
	}
	return &r, nil
}

// CompletedItems returns the keys a run already finished, so a resumed run
// skips them instead of re-burning quota on work that is done.
func (db *DB) CompletedItems(ctx context.Context, runID int64) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT item_key FROM run_items WHERE run_id = ? AND status IN (?, ?)`,
		runID, model.ItemDone, model.ItemSkipped)
	if err != nil {
		return nil, fmt.Errorf("read completed items for run %d: %w", runID, err)
	}
	defer rows.Close()

	done := make(map[string]bool)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		done[key] = true
	}
	return done, rows.Err()
}
