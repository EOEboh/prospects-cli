package store

import (
	"context"
	"errors"
	"testing"

	"github.com/EOEboh/prospects-cli/internal/model"
)

func seedBusiness(t *testing.T, db *DB, b model.Business) (int64, UpsertResult) {
	t.Helper()
	id, result, err := db.UpsertBusiness(context.Background(), &b)
	if err != nil {
		t.Fatalf("UpsertBusiness(%q): %v", b.Name, err)
	}
	return id, result
}

func TestUpsertInsertsNewBusiness(t *testing.T) {
	db := migratedDB(t)

	id, result := seedBusiness(t, db, model.Business{
		Name: "Acme Recruiting", Website: "https://acmerecruiting.com",
		City: "Austin", Source: model.OriginCSV,
	})
	if result != Inserted {
		t.Errorf("result = %s, want inserted", result)
	}
	if id == 0 {
		t.Fatal("no id returned")
	}

	// Every new business enters the outreach pipeline, so status filters work
	// without an outer join.
	var status string
	if err := db.QueryRowContext(context.Background(),
		`SELECT status FROM outreach WHERE business_id = ?`, id).Scan(&status); err != nil {
		t.Fatalf("read outreach: %v", err)
	}
	if status != string(model.StatusNotContacted) {
		t.Errorf("outreach status = %q, want not_contacted", status)
	}
}

// The whole point of dedup: the same business written many ways is one row.
func TestUpsertDedupsOnDomain(t *testing.T) {
	db := migratedDB(t)

	first, _ := seedBusiness(t, db, model.Business{
		Name: "Acme Recruiting", Website: "https://acmerecruiting.com", City: "Austin",
	})

	variants := []string{
		"https://www.acmerecruiting.com",
		"http://acmerecruiting.com/contact",
		"acmerecruiting.com",
		"HTTPS://ACMERECRUITING.COM/",
		"https://careers.acmerecruiting.com/jobs",
	}
	for _, website := range variants {
		id, result, err := db.UpsertBusiness(context.Background(), &model.Business{
			Name: "Acme Recruiting", Website: website, City: "Austin",
		})
		if err != nil {
			t.Fatalf("UpsertBusiness(%q): %v", website, err)
		}
		if id != first {
			t.Errorf("%q created id %d, want the existing %d", website, id, first)
		}
		if result == Inserted {
			t.Errorf("%q was inserted as a new business", website)
		}
	}

	if n := countRows(t, db, "businesses"); n != 1 {
		t.Errorf("%d business rows, want 1", n)
	}
}

// Businesses with no website fall back to name plus city.
func TestUpsertDedupsOnNameKeyWhenNoWebsite(t *testing.T) {
	db := migratedDB(t)

	first, _ := seedBusiness(t, db, model.Business{Name: "Bright Path Talent", City: "Austin"})

	same, result := seedBusiness(t, db, model.Business{Name: "Bright Path Talent, LLC", City: "austin"})
	if same != first {
		t.Errorf("the same name and city created id %d, want %d", same, first)
	}
	if result == Inserted {
		t.Error("a legal-suffix variant was inserted as a new business")
	}

	// A different city is a different business.
	other, result := seedBusiness(t, db, model.Business{Name: "Bright Path Talent", City: "Dallas"})
	if other == first {
		t.Error("the same name in a different city merged into one business")
	}
	if result != Inserted {
		t.Errorf("result = %s, want inserted", result)
	}
}

// This is the failure the shared-host table exists to prevent: without it,
// every Wix site collapses into one row and the prospects are lost.
func TestUpsertKeepsSharedHostBusinessesApart(t *testing.T) {
	db := migratedDB(t)

	tests := []struct{ name, website string }{
		{"Acme Recruiting", "https://acmerecruiting.wixsite.com/home"},
		{"Bright Path Talent", "https://brightpath.wixsite.com/home"},
		{"Cedar Staffing", "https://cedar.weebly.com"},
		{"Delta Talent", "https://delta.weebly.com"},
		{"Echo Hiring", "https://facebook.com/echohiring"},
		{"Foxtrot Search", "https://facebook.com/foxtrotsearch"},
	}
	ids := make(map[int64]string)
	for _, tc := range tests {
		id, result := seedBusiness(t, db, model.Business{Name: tc.name, Website: tc.website})
		if result != Inserted {
			t.Errorf("%s (%s) was not inserted: %s", tc.name, tc.website, result)
		}
		if other, clash := ids[id]; clash {
			t.Errorf("%s and %s collapsed into one row", tc.name, other)
		}
		ids[id] = tc.name
	}
	if n := countRows(t, db, "businesses"); n != len(tests) {
		t.Errorf("%d business rows, want %d", n, len(tests))
	}
}

// Re-seeding an enriched business must not wipe what enrichment learned.
func TestUpsertNeverOverwritesWithBlanks(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	id, _ := seedBusiness(t, db, model.Business{
		Name: "Acme", Website: "https://acme.com", City: "Austin",
		Email: "hello@acme.com", Phone: "+1 512 555 0100",
	})

	// A later CSV carries only the name and website.
	if _, _, err := db.UpsertBusiness(ctx, &model.Business{
		Name: "Acme", Website: "https://acme.com",
	}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}

	var email, phone, city string
	if err := db.QueryRowContext(ctx,
		`SELECT email, phone, city FROM businesses WHERE id = ?`, id).Scan(&email, &phone, &city); err != nil {
		t.Fatalf("read business: %v", err)
	}
	if email != "hello@acme.com" || phone != "+1 512 555 0100" || city != "Austin" {
		t.Errorf("blank incoming fields clobbered stored data: email=%q phone=%q city=%q", email, phone, city)
	}
}

func TestUpsertMergesNewDetails(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	id, _ := seedBusiness(t, db, model.Business{Name: "Acme", Website: "https://acme.com"})

	_, result, err := db.UpsertBusiness(ctx, &model.Business{
		Name: "Acme Recruiting", Website: "https://acme.com",
		Email: "hi@acme.com", City: "Austin",
	})
	if err != nil {
		t.Fatalf("merge upsert: %v", err)
	}
	if result != Updated {
		t.Errorf("result = %s, want updated", result)
	}

	var name, email, city string
	if err := db.QueryRowContext(ctx,
		`SELECT name, email, city FROM businesses WHERE id = ?`, id).Scan(&name, &email, &city); err != nil {
		t.Fatalf("read business: %v", err)
	}
	// The fuller name wins; both normalize to the same key regardless.
	if name != "Acme Recruiting" {
		t.Errorf("name = %q, want the fuller %q", name, "Acme Recruiting")
	}
	if email != "hi@acme.com" || city != "Austin" {
		t.Errorf("new details not merged: email=%q city=%q", email, city)
	}
}

// A re-import with nothing new should not churn updated_at.
func TestUpsertReportsUnchanged(t *testing.T) {
	db := migratedDB(t)

	b := model.Business{Name: "Acme", Website: "https://acme.com", City: "Austin"}
	seedBusiness(t, db, b)

	_, result := seedBusiness(t, db, b)
	if result != Unchanged {
		t.Errorf("result = %s, want unchanged", result)
	}
}

// Regression: importing the same file twice must be a complete no-op.
//
// Two rows describing one business with equivalent-but-unequal values — a
// different spelling of the URL, a differently cased city — used to overwrite
// each other on every pass, reporting "updated" forever and churning
// updated_at until it answered nothing.
func TestReimportingTheSameFileChangesNothing(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	rows := []model.Business{
		{Name: "Acme Recruiting", Website: "https://www.acmerecruiting.com/contact", City: "Austin", Email: "hi@acmerecruiting.com"},
		{Name: "Acme Recruiting Inc.", Website: "acmerecruiting.com", City: "Austin"},
		{Name: "Summit Search", City: "Austin"},
		{Name: "Summit Search LLC", City: "austin"},
	}

	importAll := func() map[UpsertResult]int {
		counts := make(map[UpsertResult]int)
		for i := range rows {
			b := rows[i]
			_, result, err := db.UpsertBusiness(ctx, &b)
			if err != nil {
				t.Fatalf("UpsertBusiness(%q): %v", b.Name, err)
			}
			counts[result]++
		}
		return counts
	}

	importAll() // first pass populates

	var before string
	if err := db.QueryRowContext(ctx,
		`SELECT GROUP_CONCAT(updated_at) FROM businesses ORDER BY id`).Scan(&before); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}

	second := importAll()
	if second[Updated] != 0 || second[Inserted] != 0 {
		t.Errorf("second import reported %d inserted and %d updated, want 0 and 0 (counts: %v)",
			second[Inserted], second[Updated], second)
	}

	var after string
	if err := db.QueryRowContext(ctx,
		`SELECT GROUP_CONCAT(updated_at) FROM businesses ORDER BY id`).Scan(&after); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}
	if before != after {
		t.Error("updated_at churned on a no-op re-import")
	}
}

// Equivalent spellings of the same value are not corrections.
func TestUpsertIgnoresCaseOnlyDifferences(t *testing.T) {
	db := migratedDB(t)

	seedBusiness(t, db, model.Business{
		Name: "Acme", Website: "https://acme.com", City: "Austin", Region: "TX",
		Email: "Hi@Acme.com",
	})

	_, result := seedBusiness(t, db, model.Business{
		Name: "Acme", Website: "https://acme.com", City: "austin", Region: "tx",
		Email: "hi@acme.com",
	})
	if result != Unchanged {
		t.Errorf("result = %s, want unchanged: case-only differences are not new data", result)
	}
}

// A different URL for a domain already on file is not new information.
func TestUpsertKeepsTheFirstWebsiteSpelling(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	id, _ := seedBusiness(t, db, model.Business{Name: "Acme", Website: "https://acme.com"})

	if _, result := seedBusiness(t, db, model.Business{
		Name: "Acme", Website: "https://www.acme.com/contact-us",
	}); result != Unchanged {
		t.Errorf("result = %s, want unchanged", result)
	}

	var website string
	if err := db.QueryRowContext(ctx, `SELECT website FROM businesses WHERE id = ?`, id).Scan(&website); err != nil {
		t.Fatalf("read website: %v", err)
	}
	if website != "https://acme.com" {
		t.Errorf("website = %q, want the first spelling to stand", website)
	}
}

// But a real correction still lands.
func TestUpsertTakesGenuineCorrections(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	id, _ := seedBusiness(t, db, model.Business{
		Name: "Acme", Website: "https://acme.com", Email: "old@acme.com",
	})

	if _, result := seedBusiness(t, db, model.Business{
		Name: "Acme", Website: "https://acme.com", Email: "new@acme.com",
	}); result != Updated {
		t.Errorf("result = %s, want updated", result)
	}

	var email string
	if err := db.QueryRowContext(ctx, `SELECT email FROM businesses WHERE id = ?`, id).Scan(&email); err != nil {
		t.Fatalf("read email: %v", err)
	}
	if email != "new@acme.com" {
		t.Errorf("email = %q, want new@acme.com", email)
	}
}

// "Permanent exclusion from every future run": a suppressed domain must not be
// recreated by the next seed or discover.
func TestUpsertRefusesSuppressedDomains(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx,
		`INSERT INTO suppression_domains (domain, reason, created_at) VALUES (?, ?, ?)`,
		"acme.com", "asked to be removed", FormatTime(Now())); err != nil {
		t.Fatalf("suppress domain: %v", err)
	}

	_, result, err := db.UpsertBusiness(ctx, &model.Business{
		Name: "Acme", Website: "https://www.acme.com/contact",
	})
	if err != nil {
		t.Fatalf("UpsertBusiness: %v", err)
	}
	if result != Suppressed {
		t.Errorf("result = %s, want suppressed", result)
	}
	if n := countRows(t, db, "businesses"); n != 0 {
		t.Errorf("%d business rows created for a suppressed domain, want 0", n)
	}
}

func TestUpsertRejectsRecordsWithNoIdentity(t *testing.T) {
	db := migratedDB(t)

	_, _, err := db.UpsertBusiness(context.Background(), &model.Business{Name: "  ", Website: ""})
	if !errors.Is(err, ErrNoIdentity) {
		t.Errorf("error = %v, want ErrNoIdentity", err)
	}
}

// A business first seen without a website gains one later; the domain key must
// attach so future imports dedup on it.
func TestUpsertUpgradesNameKeyMatchToDomain(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	id, _ := seedBusiness(t, db, model.Business{Name: "Acme Recruiting", City: "Austin"})

	same, result, err := db.UpsertBusiness(ctx, &model.Business{
		Name: "Acme Recruiting", City: "Austin", Website: "https://acmerecruiting.com",
	})
	if err != nil {
		t.Fatalf("upsert with website: %v", err)
	}
	if same != id {
		t.Fatalf("id %d, want the existing %d", same, id)
	}
	if result != Updated {
		t.Errorf("result = %s, want updated", result)
	}

	var domain string
	if err := db.QueryRowContext(ctx, `SELECT domain FROM businesses WHERE id = ?`, id).Scan(&domain); err != nil {
		t.Fatalf("read domain: %v", err)
	}
	if domain != "acmerecruiting.com" {
		t.Errorf("domain = %q, want acmerecruiting.com", domain)
	}

	// And a later import by website alone finds the same row.
	byWebsite, _, err := db.UpsertBusiness(ctx, &model.Business{
		Name: "Acme Recruiting", Website: "https://www.acmerecruiting.com",
	})
	if err != nil {
		t.Fatalf("upsert by website: %v", err)
	}
	if byWebsite != id {
		t.Errorf("id %d, want %d — the domain key did not attach", byWebsite, id)
	}
}

// A name collision must not merge two businesses that have different websites.
func TestUpsertDoesNotMergeDifferentDomainsWithTheSameName(t *testing.T) {
	db := migratedDB(t)

	a, _ := seedBusiness(t, db, model.Business{
		Name: "Summit Staffing", Website: "https://summitstaffing.com", City: "Austin",
	})
	b, result := seedBusiness(t, db, model.Business{
		Name: "Summit Staffing", Website: "https://summit-staffing.net", City: "Austin",
	})
	if a == b {
		t.Error("two businesses with different websites merged on their shared name")
	}
	if result != Inserted {
		t.Errorf("result = %s, want inserted", result)
	}
}

func TestCountBusinessesExcludesSuppressed(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	id, _ := seedBusiness(t, db, model.Business{Name: "Acme", Website: "https://acme.com"})
	seedBusiness(t, db, model.Business{Name: "Bright", Website: "https://bright.com"})

	if n, err := db.CountBusinesses(ctx); err != nil || n != 2 {
		t.Fatalf("CountBusinesses = %d (%v), want 2", n, err)
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO suppression (business_id, reason, created_at) VALUES (?, ?, ?)`,
		id, "asked to be removed", FormatTime(Now())); err != nil {
		t.Fatalf("suppress: %v", err)
	}

	n, err := db.CountBusinesses(ctx)
	if err != nil {
		t.Fatalf("CountBusinesses: %v", err)
	}
	if n != 1 {
		t.Errorf("CountBusinesses = %d, want 1 after suppression", n)
	}
}

func countRows(t *testing.T, db *DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}
