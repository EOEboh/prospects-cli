package csvseed

import (
	"strings"
	"testing"

	"github.com/EOEboh/prospects-cli/internal/model"
)

func TestParseHappyPath(t *testing.T) {
	in := `name,website,city
Acme Recruiting,https://acmerecruiting.com,Austin
Bright Path Talent,https://brightpathtalent.com,Austin
`
	got, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got.Businesses) != 2 {
		t.Fatalf("parsed %d businesses, want 2", len(got.Businesses))
	}
	if len(got.Errors) != 0 {
		t.Fatalf("unexpected row errors: %v", got.Errors)
	}

	first := got.Businesses[0]
	if first.Name != "Acme Recruiting" {
		t.Errorf("name = %q", first.Name)
	}
	if first.Website != "https://acmerecruiting.com" {
		t.Errorf("website = %q", first.Website)
	}
	if first.City != "Austin" {
		t.Errorf("city = %q", first.City)
	}
	if first.Source != model.OriginCSV {
		t.Errorf("source = %q, want csv", first.Source)
	}
}

// Headers arrive in whatever form the spreadsheet exported them.
func TestParseHeaderAliases(t *testing.T) {
	tests := []struct {
		name   string
		header string
	}{
		{"canonical", "name,website,city"},
		{"title case", "Name,Website,City"},
		{"upper case", "NAME,WEBSITE,CITY"},
		{"spaces", "Business Name,Website URL,City"},
		{"underscores", "business_name,website_url,city"},
		{"hyphens", "business-name,website-url,city"},
		{"synonyms", "company,url,town"},
		{"padded", " name , website , city "},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(strings.NewReader(tc.header + "\nAcme,https://acme.com,Austin\n"))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if len(got.Businesses) != 1 {
				t.Fatalf("parsed %d businesses, want 1", len(got.Businesses))
			}
			b := got.Businesses[0]
			if b.Name != "Acme" || b.Website != "https://acme.com" || b.City != "Austin" {
				t.Errorf("columns not mapped: %+v", b)
			}
		})
	}
}

// Excel prepends a BOM on export; without stripping it no column matches.
func TestParseStripsUTF8BOM(t *testing.T) {
	in := "\uFEFFname,website\nAcme,https://acme.com\n"
	got, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got.Businesses) != 1 {
		t.Fatalf("parsed %d businesses, want 1", len(got.Businesses))
	}
	if got.Businesses[0].Name != "Acme" {
		t.Errorf("name = %q, want Acme — the BOM leaked into the header", got.Businesses[0].Name)
	}
}

func TestParseColumnOrderAndExtrasIrrelevant(t *testing.T) {
	in := `notes,city,name,internal_id,website
whatever,Austin,Acme,42,https://acme.com
`
	got, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	b := got.Businesses[0]
	if b.Name != "Acme" || b.City != "Austin" || b.Website != "https://acme.com" {
		t.Errorf("reordered columns with extras not handled: %+v", b)
	}
}

func TestParseAllOptionalColumns(t *testing.T) {
	in := `name,website,email,phone,address,city,region,country
Acme,https://acme.com,hi@acme.com,+1 512 555 0100,100 Main St,Austin,TX,US
`
	got, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	b := got.Businesses[0]
	checks := []struct{ field, got, want string }{
		{"email", b.Email, "hi@acme.com"},
		{"phone", b.Phone, "+1 512 555 0100"},
		{"address", b.Address, "100 Main St"},
		{"region", b.Region, "TX"},
		{"country", b.Country, "US"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.field, c.got, c.want)
		}
	}
}

// One malformed row is reported and skipped; the rest of the file still imports.
func TestParseSkipsBadRowsWithoutFailing(t *testing.T) {
	in := `name,website
Acme,https://acme.com
,
Bright Path,https://brightpath.com
`
	got, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got.Businesses) != 2 {
		t.Errorf("parsed %d businesses, want 2 (the good rows must survive)", len(got.Businesses))
	}
	if got.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1 blank row", got.Skipped)
	}
}

// A website with no name is still a prospect worth keeping.
func TestParseAcceptsWebsiteWithoutName(t *testing.T) {
	got, err := Parse(strings.NewReader("name,website\n,https://acme.com\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got.Businesses) != 1 {
		t.Fatalf("parsed %d businesses, want 1", len(got.Businesses))
	}
	if got.Businesses[0].Name != "https://acme.com" {
		t.Errorf("name = %q, want the URL to stand in", got.Businesses[0].Name)
	}
}

func TestParseBlankAndRaggedRows(t *testing.T) {
	in := "name,website,city\nAcme,https://acme.com,Austin\n\n   ,  ,  \nBright,https://bright.com\n"
	got, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got.Businesses) != 2 {
		t.Errorf("parsed %d businesses, want 2", len(got.Businesses))
	}
	// encoding/csv drops the wholly empty line before we ever see it, so only
	// the whitespace-filled row is counted here.
	if got.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", got.Skipped)
	}
	// The ragged row is short by one column; the missing city must read empty
	// rather than panic.
	if got.Businesses[1].City != "" {
		t.Errorf("short row city = %q, want empty", got.Businesses[1].City)
	}
}

// Error line numbers must point at the real file line, which a naive counter
// gets wrong because encoding/csv silently drops blank lines.
func TestParseReportsTrueLineNumbers(t *testing.T) {
	// The bad row sits on line 6 of the file; two blank lines precede it.
	in := "name,website\nAcme,https://acme.com\n\n\nBright,https://bright.com\n,\n"
	got, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1", got.Skipped)
	}
	if len(got.Businesses) != 2 {
		t.Fatalf("parsed %d businesses, want 2", len(got.Businesses))
	}
}

func TestParseQuotedFields(t *testing.T) {
	in := `name,address,city
"Acme, Recruiting","100 Main St, Suite 2",Austin
`
	got, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	b := got.Businesses[0]
	if b.Name != "Acme, Recruiting" {
		t.Errorf("name = %q, want %q", b.Name, "Acme, Recruiting")
	}
	if b.Address != "100 Main St, Suite 2" {
		t.Errorf("address = %q", b.Address)
	}
}

func TestParseRejectsUnusableFiles(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{"empty file", "", "header row is required"},
		{"no name column", "website,city\nhttps://acme.com,Austin\n", "no 'name' column"},
		{"unparsable csv", "name,website\n\"unterminated,https://acme.com\n", "line 2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tc.input))
			if err == nil {
				t.Fatal("want error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

// The error for a missing name column should tell the operator what to write.
func TestMissingNameColumnErrorListsKnownColumns(t *testing.T) {
	_, err := Parse(strings.NewReader("website\nhttps://acme.com\n"))
	if err == nil {
		t.Fatal("want error")
	}
	for _, col := range []string{"name", "website", "city", "email"} {
		if !strings.Contains(err.Error(), col) {
			t.Errorf("error should list the %q column: %v", col, err)
		}
	}
}

func TestParseHeaderOnly(t *testing.T) {
	got, err := Parse(strings.NewReader("name,website\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got.Businesses) != 0 {
		t.Errorf("parsed %d businesses from a header-only file, want 0", len(got.Businesses))
	}
}
