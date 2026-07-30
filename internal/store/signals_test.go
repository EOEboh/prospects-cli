package store

import (
	"context"
	"testing"

	"github.com/EOEboh/prospects-cli/internal/model"
)

func record(t *testing.T, db *DB, s model.Signal) SignalWrite {
	t.Helper()
	outcome, err := db.RecordSignal(context.Background(), &s, nil)
	if err != nil {
		t.Fatalf("RecordSignal(%s=%s): %v", s.Type, s.Value, err)
	}
	return outcome
}

func businessFixture(t *testing.T, db *DB) int64 {
	t.Helper()
	id, _ := seedBusiness(t, db, model.Business{Name: "Acme", Website: "https://acme.com"})
	return id
}

// Re-observing the same value must not append a row. Weekly re-enrichment
// otherwise buries the history it exists to preserve.
func TestRecordSignalConfirmsWithoutAppending(t *testing.T) {
	db := migratedDB(t)
	id := businessFixture(t, db)

	sig := model.Signal{BusinessID: id, Source: model.SourceWebsite, Type: model.TypeContactForm, Value: "true"}

	if got := record(t, db, sig); got != SignalNew {
		t.Errorf("first write = %s, want new", got)
	}
	for i := 0; i < 5; i++ {
		if got := record(t, db, sig); got != SignalConfirmed {
			t.Errorf("repeat write = %s, want confirmed", got)
		}
	}

	if n := countRows(t, db, "signals"); n != 1 {
		t.Errorf("%d signal rows after six identical observations, want 1", n)
	}
}

func TestRecordSignalBumpsLastSeen(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	id := businessFixture(t, db)

	sig := model.Signal{BusinessID: id, Source: model.SourceWebsite, Type: model.TypeContactForm, Value: "true"}
	record(t, db, sig)

	var observedAt, firstSeen string
	if err := db.QueryRowContext(ctx,
		`SELECT observed_at, last_seen_at FROM signals WHERE business_id = ?`, id).
		Scan(&observedAt, &firstSeen); err != nil {
		t.Fatalf("read signal: %v", err)
	}

	record(t, db, sig)

	var observedAgain, lastSeen string
	if err := db.QueryRowContext(ctx,
		`SELECT observed_at, last_seen_at FROM signals WHERE business_id = ?`, id).
		Scan(&observedAgain, &lastSeen); err != nil {
		t.Fatalf("read signal: %v", err)
	}

	if observedAgain != observedAt {
		t.Error("observed_at moved: it must record when the value was FIRST seen")
	}
	if lastSeen < firstSeen {
		t.Error("last_seen_at went backwards")
	}
}

// The transition is the product: a business that starts running ads is the hot
// buying moment, and it is invisible without this history.
func TestRecordSignalPreservesTransitions(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	id := businessFixture(t, db)

	base := model.Signal{BusinessID: id, Source: model.SourceManual, Type: model.TypeRunningAds}

	notYet := base
	notYet.Value = "false"
	if got := record(t, db, notYet); got != SignalNew {
		t.Errorf("first write = %s, want new", got)
	}
	record(t, db, notYet) // confirmed the following week

	nowAdvertising := base
	nowAdvertising.Value = "true"
	if got := record(t, db, nowAdvertising); got != SignalTransition {
		t.Errorf("changed value = %s, want transition", got)
	}

	// Exactly one current belief.
	current, err := db.CurrentSignals(ctx, id)
	if err != nil {
		t.Fatalf("CurrentSignals: %v", err)
	}
	if len(current) != 1 {
		t.Fatalf("%d current signals, want 1", len(current))
	}
	if current[0].Value != "true" {
		t.Errorf("current value = %q, want true", current[0].Value)
	}

	// And the history is intact, so "when did they start" is answerable.
	history, err := db.SignalHistory(ctx, id, model.TypeRunningAds)
	if err != nil {
		t.Fatalf("SignalHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("%d history rows, want 2", len(history))
	}

	var superseded, live int
	for _, h := range history {
		if h.IsCurrent() {
			live++
		} else {
			superseded++
		}
	}
	if live != 1 || superseded != 1 {
		t.Errorf("history has %d current and %d superseded rows, want 1 and 1", live, superseded)
	}
}

// A business can run HubSpot and Calendly at once: two current facts, not one
// replacing the other.
func TestMultiValuedSignalsCoexist(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	id := businessFixture(t, db)

	for _, tool := range []string{"hubspot", "calendly", "intercom"} {
		sig := model.Signal{
			BusinessID: id, Source: model.SourceWebsite,
			Type: model.TypeAutomationTag, Value: tool,
		}
		if got := record(t, db, sig); got != SignalNew {
			t.Errorf("%s = %s, want new", tool, got)
		}
	}

	current, err := db.CurrentSignals(ctx, id)
	if err != nil {
		t.Fatalf("CurrentSignals: %v", err)
	}
	if len(current) != 3 {
		t.Errorf("%d current automation tags, want 3 — they must not supersede each other", len(current))
	}
}

// Single-valued types keep exactly one current row per source.
func TestSingleValuedSignalsSupersede(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	id := businessFixture(t, db)

	for _, hours := range []string{"48", "24", "12"} {
		record(t, db, model.Signal{
			BusinessID: id, Source: model.SourceWebsite,
			Type: model.TypeResponseTimeHours, Value: hours,
		})
	}

	current, err := db.CurrentSignals(ctx, id)
	if err != nil {
		t.Fatalf("CurrentSignals: %v", err)
	}
	if len(current) != 1 {
		t.Fatalf("%d current response-time signals, want 1", len(current))
	}
	if current[0].Value != "12" {
		t.Errorf("current value = %q, want the latest (12)", current[0].Value)
	}
	if n := countRows(t, db, "signals"); n != 3 {
		t.Errorf("%d signal rows, want 3 — the history must survive", n)
	}
}

// Manual and automated observations of the same fact are independent: a
// hand-checked value must not be silently overwritten by a crawl, or vice
// versa.
func TestSignalsAreScopedBySource(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	id := businessFixture(t, db)

	record(t, db, model.Signal{
		BusinessID: id, Source: model.SourceWebsite,
		Type: model.TypeContactForm, Value: "false",
	})
	record(t, db, model.Signal{
		BusinessID: id, Source: model.SourceManual,
		Type: model.TypeContactForm, Value: "true",
	})

	current, err := db.CurrentSignals(ctx, id)
	if err != nil {
		t.Fatalf("CurrentSignals: %v", err)
	}
	if len(current) != 2 {
		t.Errorf("%d current signals, want 2 (one per source)", len(current))
	}
}

func TestRecordSignalRejectsUnknownTypes(t *testing.T) {
	db := migratedDB(t)
	id := businessFixture(t, db)

	_, err := db.RecordSignal(context.Background(), &model.Signal{
		BusinessID: id, Source: model.SourceManual,
		Type: "running_add", Value: "true",
	}, nil)
	if err == nil {
		t.Error("an unknown signal type must be rejected before it reaches the table")
	}
}

func TestRecordSignalsCountsTransitions(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	id := businessFixture(t, db)

	initial := []model.Signal{
		{BusinessID: id, Source: model.SourceWebsite, Type: model.TypeContactForm, Value: "false"},
		{BusinessID: id, Source: model.SourceWebsite, Type: model.TypeChatWidget, Value: "false"},
	}
	if n, err := db.RecordSignals(ctx, initial, nil); err != nil || n != 0 {
		t.Fatalf("RecordSignals = %d (%v), want 0 transitions on first write", n, err)
	}

	changed := []model.Signal{
		{BusinessID: id, Source: model.SourceWebsite, Type: model.TypeContactForm, Value: "true"},
		{BusinessID: id, Source: model.SourceWebsite, Type: model.TypeChatWidget, Value: "false"},
	}
	n, err := db.RecordSignals(ctx, changed, nil)
	if err != nil {
		t.Fatalf("RecordSignals: %v", err)
	}
	if n != 1 {
		t.Errorf("counted %d transitions, want 1", n)
	}
}

func TestBusinessesToEnrich(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	withSite, _ := seedBusiness(t, db, model.Business{Name: "Acme", Website: "https://acme.com"})
	seedBusiness(t, db, model.Business{Name: "No Website", City: "Austin"})
	enriched, _ := seedBusiness(t, db, model.Business{Name: "Done", Website: "https://done.com"})

	record(t, db, model.Signal{
		BusinessID: enriched, Source: model.SourceWebsite,
		Type: model.TypeContactForm, Value: "true",
	})

	pending, err := db.BusinessesToEnrich(ctx, false, 0)
	if err != nil {
		t.Fatalf("BusinessesToEnrich: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("%d pending, want 1 (%v)", len(pending), pending)
	}
	if pending[0].ID != withSite {
		t.Errorf("pending business = %d, want %d", pending[0].ID, withSite)
	}

	// --force revisits everything that has a website.
	forced, err := db.BusinessesToEnrich(ctx, true, 0)
	if err != nil {
		t.Fatalf("BusinessesToEnrich(force): %v", err)
	}
	if len(forced) != 2 {
		t.Errorf("%d with --force, want 2 (the business with no website stays out)", len(forced))
	}
}

func TestBusinessesToEnrichExcludesSuppressed(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	id, _ := seedBusiness(t, db, model.Business{Name: "Acme", Website: "https://acme.com"})
	if _, err := db.ExecContext(ctx,
		`INSERT INTO suppression (business_id, reason, created_at) VALUES (?, ?, ?)`,
		id, "asked to be removed", FormatTime(Now())); err != nil {
		t.Fatalf("suppress: %v", err)
	}

	pending, err := db.BusinessesToEnrich(ctx, true, 0)
	if err != nil {
		t.Fatalf("BusinessesToEnrich: %v", err)
	}
	if len(pending) != 0 {
		t.Error("a suppressed business must never be fetched again")
	}
}

func TestBusinessByIDRefusesSuppressed(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	id, _ := seedBusiness(t, db, model.Business{Name: "Acme", Website: "https://acme.com"})
	if _, err := db.BusinessByID(ctx, id); err != nil {
		t.Fatalf("BusinessByID: %v", err)
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO suppression (business_id, reason, created_at) VALUES (?, ?, ?)`,
		id, "asked to be removed", FormatTime(Now())); err != nil {
		t.Fatalf("suppress: %v", err)
	}
	if _, err := db.BusinessByID(ctx, id); err == nil {
		t.Error("a suppressed business must not be loadable by id")
	}
}

// A discovered address is promoted onto the business, but never over one
// already on file.
func TestUpdateBusinessEmail(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	id, _ := seedBusiness(t, db, model.Business{Name: "Acme", Website: "https://acme.com"})
	if err := db.UpdateBusinessEmail(ctx, id, "found@acme.com"); err != nil {
		t.Fatalf("UpdateBusinessEmail: %v", err)
	}

	var email string
	if err := db.QueryRowContext(ctx, `SELECT email FROM businesses WHERE id = ?`, id).Scan(&email); err != nil {
		t.Fatalf("read email: %v", err)
	}
	if email != "found@acme.com" {
		t.Errorf("email = %q, want found@acme.com", email)
	}

	if err := db.UpdateBusinessEmail(ctx, id, "other@acme.com"); err != nil {
		t.Fatalf("UpdateBusinessEmail: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT email FROM businesses WHERE id = ?`, id).Scan(&email); err != nil {
		t.Fatalf("read email: %v", err)
	}
	if email != "found@acme.com" {
		t.Errorf("email = %q — a stored address must not be overwritten", email)
	}
}
