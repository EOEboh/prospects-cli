// Package quota keeps external API usage inside the free tier.
//
// Two independent things live here. Tier resolution answers "what will this
// request cost?" from the fields it asks for — the answer is the highest tier
// among them, so one careless field escalates the whole call. The ledger
// answers "have we already used this month's free allowance?" and refuses the
// call if so.
//
// Google's budget alerts notify after the fact and do not stop usage, so the
// stop lives here.
package quota

import (
	"fmt"
	"sort"
	"strings"
)

// Tier is a Google Maps Platform billing tier. The free allowance is per SKU
// per calendar month, not shared across SKUs.
//
// Figures published by Google in March 2025 (Essentials 10K, Pro 5K,
// Enterprise 1K). Pricing moves: PROSPECT_PLACES_MONTHLY_MAX overrides these
// without a rebuild, and the README says to verify them in the Cloud Console.
type Tier struct {
	Name        string
	Rank        int // higher wins when a field mask spans tiers
	FreeMonthly int
}

var (
	TierEssentials = Tier{Name: "essentials", Rank: 1, FreeMonthly: 10_000}
	TierPro        = Tier{Name: "pro", Rank: 2, FreeMonthly: 5_000}
	TierEnterprise = Tier{Name: "enterprise", Rank: 3, FreeMonthly: 1_000}

	// TierFree covers APIs that do not bill per call. The Meta Ad Library is
	// rate-limited rather than metered; the ceiling there exists to stay
	// polite and avoid a throttle, not to avoid a bill.
	TierFree = Tier{Name: "free", Rank: 0, FreeMonthly: 0}
)

// Providers.
const (
	ProviderPlaces  = "google_places"
	ProviderMetaAds = "meta_ads"
)

// SKU is one billable operation at one tier.
type SKU struct {
	Provider string
	Name     string
	Tier     Tier
}

func (s SKU) String() string { return s.Provider + "/" + s.Name }

// Places SKUs. Text Search and Place Details are billed independently, each
// with its own free allowance.
var (
	SKUTextSearchIDsOnly   = SKU{ProviderPlaces, "text_search_essentials_ids_only", TierEssentials}
	SKUTextSearchPro       = SKU{ProviderPlaces, "text_search_pro", TierPro}
	SKUTextSearchEnterpise = SKU{ProviderPlaces, "text_search_enterprise", TierEnterprise}

	SKUPlaceDetailsIDsOnly    = SKU{ProviderPlaces, "place_details_essentials_ids_only", TierEssentials}
	SKUPlaceDetailsPro        = SKU{ProviderPlaces, "place_details_pro", TierPro}
	SKUPlaceDetailsEnterprise = SKU{ProviderPlaces, "place_details_enterprise", TierEnterprise}

	// SKUAdsArchive is not metered by Meta, but a local ceiling keeps a
	// runaway loop from tripping their rate limiter.
	SKUAdsArchive = SKU{ProviderMetaAds, "ads_archive", TierFree}
)

// AllSKUs is what `prospect quota` reports on.
func AllSKUs() []SKU {
	return []SKU{
		SKUTextSearchIDsOnly, SKUTextSearchPro, SKUTextSearchEnterpise,
		SKUPlaceDetailsIDsOnly, SKUPlaceDetailsPro, SKUPlaceDetailsEnterprise,
		SKUAdsArchive,
	}
}

// Field-to-tier mapping for the Places API (New), from Google's published SKU
// field lists. Only the fields this tool could plausibly want are listed;
// anything unrecognized resolves to Enterprise, because guessing cheap on an
// unknown field is how a surprise bill happens.
var (
	fieldsIDsOnly = map[string]bool{
		"places.id": true, "places.name": true, "places.attributions": true,
		"nextPageToken": true,
		// Place Details returns these unprefixed.
		"id": true, "name": true, "attributions": true,
	}

	fieldsPro = map[string]bool{
		"places.displayName": true, "places.formattedAddress": true,
		"places.shortFormattedAddress": true, "places.addressComponents": true,
		"places.location": true, "places.types": true, "places.primaryType": true,
		"places.primaryTypeDisplayName": true, "places.businessStatus": true,
		"places.googleMapsUri": true, "places.viewport": true, "places.plusCode": true,
		"places.adrFormatAddress": true, "places.iconBackgroundColor": true,
		"places.iconMaskBaseUri": true, "places.utcOffsetMinutes": true,
		"places.timeZone": true, "places.photos": true,

		"displayName": true, "formattedAddress": true, "shortFormattedAddress": true,
		"addressComponents": true, "location": true, "types": true,
		"primaryType": true, "primaryTypeDisplayName": true, "businessStatus": true,
		"googleMapsUri": true, "viewport": true, "plusCode": true,
		"adrFormatAddress": true, "utcOffsetMinutes": true, "timeZone": true,
	}

	fieldsEnterprise = map[string]bool{
		// websiteUri is the one that matters here: it is this tool's dedup key
		// and the target `enrich` fetches, so discovery cannot avoid Enterprise.
		"places.websiteUri": true, "places.rating": true, "places.userRatingCount": true,
		"places.nationalPhoneNumber": true, "places.internationalPhoneNumber": true,
		"places.priceLevel": true, "places.priceRange": true,
		"places.regularOpeningHours": true, "places.currentOpeningHours": true,

		"websiteUri": true, "rating": true, "userRatingCount": true,
		"nationalPhoneNumber": true, "internationalPhoneNumber": true,
		"priceLevel": true, "priceRange": true,
		"regularOpeningHours": true, "currentOpeningHours": true,
	}
)

// TierForFields resolves the billing tier of a field mask. Billing is at the
// highest tier among the fields requested, so this returns the maximum, along
// with the field responsible — which is what makes a cost warning actionable
// instead of just alarming.
func TierForFields(fields []string) (Tier, string, error) {
	if len(fields) == 0 {
		return Tier{}, "", fmt.Errorf("empty field mask: Places bills the full response when no mask is sent")
	}

	tier, culprit := TierEssentials, ""
	for _, raw := range fields {
		f := strings.TrimSpace(raw)
		if f == "" {
			continue
		}
		var ft Tier
		switch {
		case fieldsIDsOnly[f]:
			ft = TierEssentials
		case fieldsPro[f]:
			ft = TierPro
		case fieldsEnterprise[f]:
			ft = TierEnterprise
		default:
			// Unknown fields bill as Enterprise rather than optimistically
			// cheap: an unrecognized field is far more likely to be a new
			// premium one than a new free one.
			ft = TierEnterprise
		}
		if ft.Rank > tier.Rank {
			tier, culprit = ft, f
		}
	}
	return tier, culprit, nil
}

// TextSearchSKU resolves the SKU a Text Search field mask will be billed at.
func TextSearchSKU(fields []string) (SKU, string, error) {
	tier, culprit, err := TierForFields(fields)
	if err != nil {
		return SKU{}, "", err
	}
	switch tier.Rank {
	case TierEssentials.Rank:
		return SKUTextSearchIDsOnly, culprit, nil
	case TierPro.Rank:
		return SKUTextSearchPro, culprit, nil
	default:
		return SKUTextSearchEnterpise, culprit, nil
	}
}

// PlaceDetailsSKU resolves the SKU a Place Details field mask will be billed at.
func PlaceDetailsSKU(fields []string) (SKU, string, error) {
	tier, culprit, err := TierForFields(fields)
	if err != nil {
		return SKU{}, "", err
	}
	switch tier.Rank {
	case TierEssentials.Rank:
		return SKUPlaceDetailsIDsOnly, culprit, nil
	case TierPro.Rank:
		return SKUPlaceDetailsPro, culprit, nil
	default:
		return SKUPlaceDetailsEnterprise, culprit, nil
	}
}

// DefaultTextSearchMask is what `discover` requests.
//
// websiteUri already forces the Enterprise tier, and rating with
// userRatingCount sit in that same tier — so the size heuristic rides along at
// no additional cost. Dropping them would save nothing.
//
// Deliberately absent: phone numbers and opening hours. Also Enterprise, also
// free at this point, but unused by scoring, and an unused field is a field
// that gets quietly depended on later.
func DefaultTextSearchMask() []string {
	return []string{
		"places.id",
		"places.displayName",
		"places.formattedAddress",
		"places.websiteUri",
		"places.rating",
		"places.userRatingCount",
		"nextPageToken",
	}
}

// FieldMaskHeader renders a mask for the X-Goog-FieldMask header.
func FieldMaskHeader(fields []string) string {
	out := append([]string(nil), fields...)
	sort.Strings(out)
	return strings.Join(out, ",")
}
