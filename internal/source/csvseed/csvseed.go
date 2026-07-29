// Package csvseed imports businesses from a hand-built CSV.
//
// This is the zero-API path and a first-class source: it exists so the scoring
// engine can be exercised end to end before any billing is enabled.
package csvseed

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/EOEboh/prospects-cli/internal/model"
)

// Column aliases. Spreadsheets export headers in whatever form the operator
// typed, so matching is case-insensitive and ignores spaces, underscores and
// hyphens: "Business Name", "business_name" and "name" are the same column.
var columnAliases = map[string]string{
	"name": "name", "businessname": "name", "company": "name", "companyname": "name",

	"website": "website", "url": "website", "site": "website", "web": "website",
	"websiteurl": "website", "homepage": "website",

	"city": "city", "town": "city", "locality": "city",

	"email": "email", "emailaddress": "email", "contactemail": "email",

	"phone": "phone", "phonenumber": "phone", "telephone": "phone", "tel": "phone",

	"address": "address", "streetaddress": "address", "street": "address",
	"formattedaddress": "address",

	"region": "region", "state": "region", "province": "region",

	"country": "country",
}

// RowError is a single bad row. One malformed row never fails an import.
type RowError struct {
	Line int
	Err  error
}

func (e RowError) Error() string { return fmt.Sprintf("line %d: %v", e.Line, e.Err) }

// Result reports what a parse produced.
type Result struct {
	Businesses []model.Business
	Errors     []RowError
	Skipped    int // blank rows, not worth reporting individually
}

// Parse reads businesses from CSV. A header row is required, since positional
// columns in a hand-maintained file are a silent data-corruption hazard.
//
// Unknown columns are ignored, so an export with extra fields works untouched.
func Parse(r io.Reader) (*Result, error) {
	cr := csv.NewReader(stripBOM(r))
	cr.TrimLeadingSpace = true
	// Ragged rows are tolerated: a trailing comma in one row should not fail
	// the whole file.
	cr.FieldsPerRecord = -1

	header, err := cr.Read()
	if err == io.EOF {
		return nil, fmt.Errorf("empty file: a header row is required")
	}
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

	index, err := mapColumns(header)
	if err != nil {
		return nil, err
	}

	result := &Result{}
	for {
		record, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			// A parse error is positional; recording it and continuing would
			// mean guessing where the reader resumed.
			var parseErr *csv.ParseError
			if errors.As(err, &parseErr) {
				return nil, fmt.Errorf("line %d: %w", parseErr.Line, parseErr.Err)
			}
			return nil, err
		}

		// encoding/csv skips wholly empty lines without returning a record, so
		// a counter would drift from the file. FieldPos reports where the
		// record actually came from, which is what the operator has to go fix.
		line, _ := cr.FieldPos(0)

		if isBlank(record) {
			result.Skipped++
			continue
		}

		b, err := recordToBusiness(record, index)
		if err != nil {
			result.Errors = append(result.Errors, RowError{Line: line, Err: err})
			continue
		}
		result.Businesses = append(result.Businesses, b)
	}
	return result, nil
}

// mapColumns resolves header names to field positions.
func mapColumns(header []string) (map[string]int, error) {
	index := make(map[string]int)
	for i, raw := range header {
		if field, ok := columnAliases[normalizeHeader(raw)]; ok {
			// First occurrence wins; a duplicated column is the operator's
			// problem, not a reason to fail.
			if _, seen := index[field]; !seen {
				index[field] = i
			}
		}
	}
	if _, ok := index["name"]; !ok {
		return nil, fmt.Errorf(
			"no 'name' column found in header %q; recognised columns: %s",
			strings.Join(header, ","), strings.Join(knownColumns(), ", "))
	}
	return index, nil
}

func recordToBusiness(record []string, index map[string]int) (model.Business, error) {
	get := func(field string) string {
		i, ok := index[field]
		if !ok || i >= len(record) {
			return ""
		}
		return strings.TrimSpace(record[i])
	}

	name := get("name")
	website := get("website")
	if name == "" && website == "" {
		return model.Business{}, fmt.Errorf("row has neither a name nor a website")
	}
	if name == "" {
		// A website with no name is still a prospect; enrichment will fill in
		// a better name later.
		name = website
	}

	return model.Business{
		Name:    name,
		Website: website,
		Email:   get("email"),
		Phone:   get("phone"),
		Address: get("address"),
		City:    get("city"),
		Region:  get("region"),
		Country: get("country"),
		Source:  model.OriginCSV,
	}, nil
}

// normalizeHeader reduces a header cell to its comparable form, so "Business
// Name", "business_name" and "BUSINESS-NAME" all match.
func normalizeHeader(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func knownColumns() []string {
	seen := make(map[string]bool)
	var out []string
	for _, field := range columnAliases {
		if !seen[field] {
			seen[field] = true
			out = append(out, field)
		}
	}
	sort.Strings(out)
	return out
}

func isBlank(record []string) bool {
	for _, f := range record {
		if strings.TrimSpace(f) != "" {
			return false
		}
	}
	return true
}

// stripBOM removes the UTF-8 byte order mark Excel prepends on export. Without
// this the first header cell parses as "\uFEFFname" and matches no column — the
// single most common reason a perfectly good spreadsheet fails to import.
func stripBOM(r io.Reader) io.Reader {
	const bom = "\uFEFF"
	buf := make([]byte, len(bom))

	n, err := io.ReadFull(r, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		// Let the CSV reader surface the failure with its own context.
		return io.MultiReader(strings.NewReader(string(buf[:n])), r)
	}
	if string(buf[:n]) == bom {
		return r
	}
	return io.MultiReader(strings.NewReader(string(buf[:n])), r)
}
