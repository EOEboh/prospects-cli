package quota

import (
	"strings"
	"testing"
)

// The whole cost model rests on this: billing is at the HIGHEST tier among the
// requested fields, so one careless field escalates an entire call.
func TestTierForFields(t *testing.T) {
	tests := []struct {
		name        string
		fields      []string
		wantTier    Tier
		wantCulprit string
	}{
		{
			name:     "ids only stays essentials",
			fields:   []string{"places.id", "places.name", "nextPageToken"},
			wantTier: TierEssentials,
		},
		{
			name:        "display name is pro",
			fields:      []string{"places.id", "places.displayName"},
			wantTier:    TierPro,
			wantCulprit: "places.displayName",
		},
		{
			name:        "websiteUri alone forces enterprise",
			fields:      []string{"places.id", "places.websiteUri"},
			wantTier:    TierEnterprise,
			wantCulprit: "places.websiteUri",
		},
		{
			name: "ratings alongside websiteUri cost nothing extra",
			fields: []string{
				"places.id", "places.displayName", "places.formattedAddress",
				"places.websiteUri", "places.rating", "places.userRatingCount",
			},
			wantTier:    TierEnterprise,
			wantCulprit: "places.websiteUri",
		},
		{
			name:        "unknown fields bill as enterprise, not optimistically cheap",
			fields:      []string{"places.id", "places.somethingGoogleAddedLastWeek"},
			wantTier:    TierEnterprise,
			wantCulprit: "places.somethingGoogleAddedLastWeek",
		},
		{
			name:     "unprefixed place details fields resolve too",
			fields:   []string{"id", "displayName"},
			wantTier: TierPro,
		},
		{
			name:     "blank entries are ignored",
			fields:   []string{"places.id", "", "  "},
			wantTier: TierEssentials,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tier, culprit, err := TierForFields(tc.fields)
			if err != nil {
				t.Fatalf("TierForFields(%v): %v", tc.fields, err)
			}
			if tier != tc.wantTier {
				t.Errorf("tier = %s, want %s", tier.Name, tc.wantTier.Name)
			}
			if tc.wantCulprit != "" && culprit != tc.wantCulprit {
				t.Errorf("culprit = %q, want %q", culprit, tc.wantCulprit)
			}
		})
	}
}

// An empty mask makes Places return and bill the full response.
func TestTierForFieldsRejectsEmptyMask(t *testing.T) {
	if _, _, err := TierForFields(nil); err == nil {
		t.Error("an empty field mask must be refused")
	}
}

func TestTextSearchSKU(t *testing.T) {
	tests := []struct {
		name   string
		fields []string
		want   SKU
	}{
		{"ids only", []string{"places.id"}, SKUTextSearchIDsOnly},
		{"pro", []string{"places.displayName"}, SKUTextSearchPro},
		{"enterprise", []string{"places.websiteUri"}, SKUTextSearchEnterpise},
		{"default mask", DefaultTextSearchMask(), SKUTextSearchEnterpise},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := TextSearchSKU(tc.fields)
			if err != nil {
				t.Fatalf("TextSearchSKU: %v", err)
			}
			if got != tc.want {
				t.Errorf("SKU = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestPlaceDetailsSKU(t *testing.T) {
	tests := []struct {
		fields []string
		want   SKU
	}{
		{[]string{"id"}, SKUPlaceDetailsIDsOnly},
		{[]string{"displayName", "formattedAddress"}, SKUPlaceDetailsPro},
		{[]string{"displayName", "websiteUri"}, SKUPlaceDetailsEnterprise},
	}
	for _, tc := range tests {
		got, _, err := PlaceDetailsSKU(tc.fields)
		if err != nil {
			t.Fatalf("PlaceDetailsSKU(%v): %v", tc.fields, err)
		}
		if got != tc.want {
			t.Errorf("PlaceDetailsSKU(%v) = %s, want %s", tc.fields, got, tc.want)
		}
	}
}

// The default mask must carry what the pipeline actually needs — the website
// above all, since it is the dedup key and what enrich fetches — and nothing
// beyond the tier that already forces.
func TestDefaultTextSearchMask(t *testing.T) {
	mask := DefaultTextSearchMask()
	joined := strings.Join(mask, ",")

	for _, required := range []string{
		"places.id", "places.displayName", "places.websiteUri",
		"places.rating", "places.userRatingCount",
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("default mask is missing %s", required)
		}
	}

	// Enterprise fields the scoring engine does not read should stay out: an
	// unused field becomes a quietly load-bearing one.
	for _, unwanted := range []string{
		"places.nationalPhoneNumber", "places.regularOpeningHours", "places.priceLevel",
	} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("default mask requests unused field %s", unwanted)
		}
	}
}

func TestFieldMaskHeaderIsStable(t *testing.T) {
	// A stable rendering keeps the HTTP cache key stable across runs.
	a := FieldMaskHeader([]string{"places.websiteUri", "places.id"})
	b := FieldMaskHeader([]string{"places.id", "places.websiteUri"})
	if a != b {
		t.Errorf("field mask ordering leaked into the header: %q vs %q", a, b)
	}
	if strings.Contains(a, " ") {
		t.Errorf("header must not contain spaces: %q", a)
	}
}
