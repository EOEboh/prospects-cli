package cli

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"

	"github.com/EOEboh/prospects-cli/internal/model"
	"github.com/EOEboh/prospects-cli/internal/store"
)

func exportFixture() []store.ScoredBusiness {
	return []store.ScoredBusiness{{
		Business: model.Business{
			ID: 1, Name: "Acme Recruiting", Website: "https://acmerecruiting.com",
			Email: "hello@acmerecruiting.com", City: "Austin", Region: "TX",
		},
		Score: model.Score{
			Score: 77, Raw: 85, MaxPossible: 110, Confidence: 0.77,
			NeedsManualCheck: false,
			Explanation:      "Acme Recruiting scores 77/100. It is currently running paid ads.",
			Breakdown: []model.Component{
				{Rule: "running_ads", Label: "Currently running ads", Points: 40, Evidence: "checked ad library"},
				{Rule: "public_email", Label: "Public contact email found", Points: 10, Evidence: "hello@acmerecruiting.com"},
			},
		},
		Status: model.StatusNotContacted,
	}}
}

// The reasoning has to travel with the data: a row pasted into a spreadsheet
// should still say why it is there.
func TestWriteCSVCarriesTheExplanation(t *testing.T) {
	var buf bytes.Buffer
	if err := writeCSV(&buf, exportFixture()); err != nil {
		t.Fatalf("writeCSV: %v", err)
	}

	records, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("the output is not valid CSV: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("%d records, want a header and one row", len(records))
	}

	header, row := records[0], records[1]
	if len(header) != len(exportColumns) {
		t.Errorf("header has %d columns, want %d", len(header), len(exportColumns))
	}

	fields := make(map[string]string, len(header))
	for i, name := range header {
		fields[name] = row[i]
	}

	checks := map[string]string{
		"id":             "1",
		"name":           "Acme Recruiting",
		"score":          "77",
		"email":          "hello@acmerecruiting.com",
		"status":         "not_contacted",
		"confidence":     "0.77",
		"needs_ad_check": "false",
		"raw_score":      "85",
		"max_possible":   "110",
	}
	for column, want := range checks {
		if fields[column] != want {
			t.Errorf("%s = %q, want %q", column, fields[column], want)
		}
	}

	if !strings.Contains(fields["explanation"], "currently running paid ads") {
		t.Errorf("explanation = %q, want the reasoning", fields["explanation"])
	}
	if !strings.Contains(fields["top_signals"], "Currently running ads +40") {
		t.Errorf("top_signals = %q, want the breakdown flattened", fields["top_signals"])
	}
}

// Explanations contain commas and quotes; encoding/csv must be doing the
// escaping, not string concatenation.
func TestWriteCSVEscapesProse(t *testing.T) {
	rows := exportFixture()
	rows[0].Score.Explanation = `It promises "a reply within 24 hours", which is slow, and it lists an email.`
	rows[0].Business.Name = `Acme, Recruiting "Ltd"`

	var buf bytes.Buffer
	if err := writeCSV(&buf, rows); err != nil {
		t.Fatalf("writeCSV: %v", err)
	}

	records, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("commas and quotes broke the CSV: %v", err)
	}
	if records[1][1] != `Acme, Recruiting "Ltd"` {
		t.Errorf("name round-tripped as %q", records[1][1])
	}
	if records[1][3] != rows[0].Score.Explanation {
		t.Errorf("explanation round-tripped as %q", records[1][3])
	}
}

// JSON keeps the breakdown structured, which CSV cannot.
func TestWriteJSONKeepsTheBreakdownStructured(t *testing.T) {
	var buf bytes.Buffer
	if err := writeJSON(&buf, exportFixture()); err != nil {
		t.Fatalf("writeJSON: %v", err)
	}

	var out []exportRow
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("the output is not valid JSON: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("%d rows, want 1", len(out))
	}

	row := out[0]
	if row.Score != 77 || row.Name != "Acme Recruiting" {
		t.Errorf("row = %+v", row)
	}
	if len(row.Breakdown) != 2 {
		t.Fatalf("%d components, want 2", len(row.Breakdown))
	}
	if row.Breakdown[0].Rule != "running_ads" || row.Breakdown[0].Points != 40 {
		t.Errorf("first component = %+v", row.Breakdown[0])
	}
	if row.Breakdown[0].Evidence != "checked ad library" {
		t.Errorf("evidence lost: %+v", row.Breakdown[0])
	}
}

// An empty export must still be a valid file, not an empty one: a header-only
// CSV and an empty JSON array both parse.
func TestExportEmptyResultsAreStillValid(t *testing.T) {
	var csvBuf bytes.Buffer
	if err := writeCSV(&csvBuf, nil); err != nil {
		t.Fatalf("writeCSV: %v", err)
	}
	records, err := csv.NewReader(&csvBuf).ReadAll()
	if err != nil {
		t.Fatalf("empty CSV is malformed: %v", err)
	}
	if len(records) != 1 {
		t.Errorf("%d records, want just the header", len(records))
	}

	var jsonBuf bytes.Buffer
	if err := writeJSON(&jsonBuf, nil); err != nil {
		t.Fatalf("writeJSON: %v", err)
	}
	var out []exportRow
	if err := json.Unmarshal(jsonBuf.Bytes(), &out); err != nil {
		t.Fatalf("empty JSON is malformed: %v", err)
	}
	if out == nil {
		t.Error("want an empty array, not null: a consumer should not have to special-case it")
	}
}

func TestWrapText(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		width int
		want  []string
	}{
		{"short text stays on one line", "hello world", 40, []string{"hello world"}},
		{"wraps at the width", "aaa bbb ccc ddd", 7, []string{"aaa bbb", "ccc ddd"}},
		{"collapses whitespace", "a   b\n\nc", 40, []string{"a b c"}},
		{"empty", "", 40, nil},
		{"a word longer than the width is not split", "supercalifragilistic x", 10,
			[]string{"supercalifragilistic", "x"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapText(tc.text, tc.width)
			if len(got) != len(tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("got %q, want %q", got, tc.want)
					break
				}
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"shorter than the budget", "short", 10, "short"},
		{"exactly the budget", "exactly_10", 10, "exactly_10"},
		{"longer is cut with an ellipsis", "this is far too long", 10, "this is f…"},
		// Names are measured in runes, not bytes, so an accented name is not
		// cut mid-character.
		{"multi-byte name", "Café Ampère Recruiting", 8, "Café Am…"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncate(tc.in, tc.max)
			if got != tc.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
			if len([]rune(got)) > tc.max {
				t.Errorf("result is %d runes, over the %d budget", len([]rune(got)), tc.max)
			}
		})
	}
}
