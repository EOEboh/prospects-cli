package places

import (
	"strings"

	"github.com/EOEboh/prospects-cli/internal/model"
)

type searchRequest struct {
	TextQuery string `json:"textQuery"`
	PageSize  int    `json:"pageSize,omitempty"`
	PageToken string `json:"pageToken,omitempty"`
}

type searchResponse struct {
	Places        []place `json:"places"`
	NextPageToken string  `json:"nextPageToken"`
}

// place mirrors only the fields the default mask requests. Anything absent
// here is a field we are deliberately not paying for.
type place struct {
	ID               string             `json:"id"`
	DisplayName      localizedText      `json:"displayName"`
	FormattedAddress string             `json:"formattedAddress"`
	WebsiteURI       string             `json:"websiteUri"`
	Rating           *float64           `json:"rating"`
	UserRatingCount  *int               `json:"userRatingCount"`
	AddressComponent []addressComponent `json:"addressComponents"`
}

type localizedText struct {
	Text         string `json:"text"`
	LanguageCode string `json:"languageCode"`
}

type addressComponent struct {
	LongText  string   `json:"longText"`
	ShortText string   `json:"shortText"`
	Types     []string `json:"types"`
}

// toBusiness maps a Places result onto a business row.
//
// City, region and country come from addressComponents rather than by splitting
// formattedAddress on commas: address formats vary by country, and the city is
// what scopes the fallback dedup key. Getting it wrong merges unrelated
// businesses.
func (p place) toBusiness() model.Business {
	b := model.Business{
		Name:        strings.TrimSpace(p.DisplayName.Text),
		Website:     strings.TrimSpace(p.WebsiteURI),
		Address:     strings.TrimSpace(p.FormattedAddress),
		PlaceID:     strings.TrimSpace(p.ID),
		Rating:      p.Rating,
		ReviewCount: p.UserRatingCount,
		Source:      model.OriginPlaces,
	}

	for _, c := range p.AddressComponent {
		switch {
		case hasType(c.Types, "locality"), hasType(c.Types, "postal_town"):
			if b.City == "" {
				b.City = c.LongText
			}
		case hasType(c.Types, "administrative_area_level_1"):
			if b.Region == "" {
				// The short form is the usable one: "TX" rather than "Texas".
				b.Region = firstNonEmpty(c.ShortText, c.LongText)
			}
		case hasType(c.Types, "country"):
			if b.Country == "" {
				b.Country = firstNonEmpty(c.ShortText, c.LongText)
			}
		}
	}

	// Some results carry a sublocality but no locality; without a city the
	// name key loses its scope, so fall back before giving up.
	if b.City == "" {
		for _, c := range p.AddressComponent {
			if hasType(c.Types, "sublocality") || hasType(c.Types, "administrative_area_level_2") {
				b.City = c.LongText
				break
			}
		}
	}

	return b
}

func hasType(types []string, want string) bool {
	for _, t := range types {
		if t == want {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
