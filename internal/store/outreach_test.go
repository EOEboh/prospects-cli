package store

import (
	"context"
	"testing"

	"github.com/EOEboh/prospects-cli/internal/model"
)

// A business starts in the pipeline as soon as it exists, so status filters
// need no outer join.
func TestNewBusinessStartsNotContacted(t *testing.T) {
	db := migratedDB(t)
	id := businessFixture(t, db)

	o, err := db.OutreachFor(context.Background(), id)
	if err != nil {
		t.Fatalf("OutreachFor: %v", err)
	}
	if o.Status != model.StatusNotContacted {
		t.Errorf("status = %q, want not_contacted", o.Status)
	}
}

// The sequence of touches has to be reconstructable: "emailed twice, no reply"
// is a different situation from "emailed once last week".
func TestOutreachHistoryRecordsEveryTransition(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	id := businessFixture(t, db)

	steps := []struct {
		to   model.OutreachStatus
		note string
	}{
		{model.StatusEmailed1, "form observation angle"},
		{model.StatusEmailed2, "followed up"},
		{model.StatusReplied, "asked for pricing"},
		{model.StatusCallBooked, "Tuesday 10am"},
	}
	for _, step := range steps {
		if _, err := db.SetOutreachStatus(ctx, id, step.to, step.note); err != nil {
			t.Fatalf("SetOutreachStatus(%s): %v", step.to, err)
		}
	}

	history, err := db.OutreachHistory(ctx, id)
	if err != nil {
		t.Fatalf("OutreachHistory: %v", err)
	}
	if len(history) != len(steps) {
		t.Fatalf("%d history entries, want %d", len(history), len(steps))
	}

	// Oldest first, and each transition knows where it came from.
	if history[0].From != model.StatusNotContacted {
		t.Errorf("first transition came from %q, want not_contacted", history[0].From)
	}
	if history[0].To != model.StatusEmailed1 {
		t.Errorf("first transition went to %q, want emailed_1", history[0].To)
	}
	if history[3].From != model.StatusReplied || history[3].To != model.StatusCallBooked {
		t.Errorf("last transition = %s → %s, want replied → call_booked", history[3].From, history[3].To)
	}
	if history[0].Note != "form observation angle" {
		t.Errorf("note = %q, want it preserved", history[0].Note)
	}

	current, err := db.OutreachFor(ctx, id)
	if err != nil {
		t.Fatalf("OutreachFor: %v", err)
	}
	if current.Status != model.StatusCallBooked {
		t.Errorf("current status = %q, want call_booked", current.Status)
	}
}

// SetOutreachStatus reports the previous status so the caller can say what
// actually changed.
func TestSetOutreachStatusReportsPrevious(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	id := businessFixture(t, db)

	from, err := db.SetOutreachStatus(ctx, id, model.StatusEmailed1, "")
	if err != nil {
		t.Fatalf("SetOutreachStatus: %v", err)
	}
	if from != model.StatusNotContacted {
		t.Errorf("from = %q, want not_contacted", from)
	}

	from, err = db.SetOutreachStatus(ctx, id, model.StatusReplied, "")
	if err != nil {
		t.Fatalf("SetOutreachStatus: %v", err)
	}
	if from != model.StatusEmailed1 {
		t.Errorf("from = %q, want emailed_1", from)
	}
}

// An empty note must not erase one already recorded.
func TestSetOutreachStatusKeepsExistingNote(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	id := businessFixture(t, db)

	if _, err := db.SetOutreachStatus(ctx, id, model.StatusEmailed1, "the angle that worked"); err != nil {
		t.Fatalf("SetOutreachStatus: %v", err)
	}
	if _, err := db.SetOutreachStatus(ctx, id, model.StatusEmailed2, ""); err != nil {
		t.Fatalf("SetOutreachStatus: %v", err)
	}

	o, err := db.OutreachFor(ctx, id)
	if err != nil {
		t.Fatalf("OutreachFor: %v", err)
	}
	if o.Notes != "the angle that worked" {
		t.Errorf("notes = %q, want the earlier note preserved", o.Notes)
	}
}

// "Once suppressed, always excluded" has to survive the business being
// recreated, which is why the domain is suppressed alongside the row.
func TestSuppressCoversTheDomain(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	id, _ := seedBusiness(t, db, model.Business{Name: "Acme", Website: "https://acme.com"})

	domain, err := db.Suppress(ctx, id, "asked to be removed", false)
	if err != nil {
		t.Fatalf("Suppress: %v", err)
	}
	if domain != "acme.com" {
		t.Errorf("suppressed domain = %q, want acme.com", domain)
	}

	// Gone from every listing.
	if n, err := db.CountBusinesses(ctx); err != nil || n != 0 {
		t.Errorf("CountBusinesses = %d (%v), want 0", n, err)
	}

	// And a re-import does not bring it back.
	_, result, err := db.UpsertBusiness(ctx, &model.Business{
		Name: "Acme", Website: "https://www.acme.com/contact",
	})
	if err != nil {
		t.Fatalf("UpsertBusiness: %v", err)
	}
	if result != Suppressed {
		t.Errorf("re-import result = %s, want suppressed", result)
	}
}

// --id-only suppresses the row but leaves the domain free, which is the
// documented (and rarely wanted) behaviour.
func TestSuppressIDOnlyLeavesDomainOpen(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	id, _ := seedBusiness(t, db, model.Business{Name: "Acme", Website: "https://acme.com"})
	if _, err := db.Suppress(ctx, id, "wrong contact", true); err != nil {
		t.Fatalf("Suppress: %v", err)
	}

	var suppressedDomains int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM suppression_domains`).Scan(&suppressedDomains); err != nil {
		t.Fatalf("count suppression_domains: %v", err)
	}
	if suppressedDomains != 0 {
		t.Error("--id-only must not suppress the domain")
	}

	// The original row is still excluded from listings.
	if n, err := db.CountBusinesses(ctx); err != nil || n != 0 {
		t.Errorf("CountBusinesses = %d (%v), want 0", n, err)
	}
}

// The outreach history should explain why a prospect stopped rather than
// showing it simply going quiet.
func TestSuppressClosesTheOutreachEntry(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	id, _ := seedBusiness(t, db, model.Business{Name: "Acme", Website: "https://acme.com"})
	if _, err := db.SetOutreachStatus(ctx, id, model.StatusEmailed1, "first touch"); err != nil {
		t.Fatalf("SetOutreachStatus: %v", err)
	}
	if _, err := db.Suppress(ctx, id, "asked to be removed", false); err != nil {
		t.Fatalf("Suppress: %v", err)
	}

	o, err := db.OutreachFor(ctx, id)
	if err != nil {
		t.Fatalf("OutreachFor: %v", err)
	}
	if o.Status != model.StatusDead {
		t.Errorf("status = %q, want dead", o.Status)
	}

	history, err := db.OutreachHistory(ctx, id)
	if err != nil {
		t.Fatalf("OutreachHistory: %v", err)
	}
	last := history[len(history)-1]
	if last.To != model.StatusDead {
		t.Errorf("last transition went to %q, want dead", last.To)
	}
	if last.Note == "" {
		t.Error("the suppression reason should appear in the history")
	}
}

func TestIsSuppressed(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	id, _ := seedBusiness(t, db, model.Business{Name: "Acme", Website: "https://acme.com"})
	if suppressed, _, err := db.IsSuppressed(ctx, id); err != nil || suppressed {
		t.Fatalf("IsSuppressed = %v (%v), want false", suppressed, err)
	}

	if _, err := db.Suppress(ctx, id, "asked to be removed", false); err != nil {
		t.Fatalf("Suppress: %v", err)
	}
	suppressed, reason, err := db.IsSuppressed(ctx, id)
	if err != nil {
		t.Fatalf("IsSuppressed: %v", err)
	}
	if !suppressed || reason != "asked to be removed" {
		t.Errorf("IsSuppressed = (%v, %q), want (true, %q)", suppressed, reason, "asked to be removed")
	}
}

// AnyBusinessByID exists so suppress and status can still act on a suppressed
// business, which BusinessByID deliberately refuses to load.
func TestAnyBusinessByIDSeesSuppressed(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	id, _ := seedBusiness(t, db, model.Business{Name: "Acme", Website: "https://acme.com"})
	if _, err := db.Suppress(ctx, id, "asked to be removed", false); err != nil {
		t.Fatalf("Suppress: %v", err)
	}

	if _, err := db.BusinessByID(ctx, id); err == nil {
		t.Error("BusinessByID should refuse a suppressed business")
	}
	b, err := db.AnyBusinessByID(ctx, id)
	if err != nil {
		t.Fatalf("AnyBusinessByID: %v", err)
	}
	if b.Name != "Acme" {
		t.Errorf("name = %q", b.Name)
	}
}
