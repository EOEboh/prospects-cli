package model

import "time"

// Origin records which source first created a business row.
type Origin string

const (
	OriginCSV    Origin = "csv"
	OriginPlaces Origin = "places"
	OriginManual Origin = "manual"
)

// Business is a prospect's identity and contact details.
//
// Dedup runs on Domain first and NameKey second. Both are normalized forms
// computed at write time, never user input — see internal/dedup.
type Business struct {
	ID      int64
	Name    string
	Website string

	// Domain is the normalized dedup key derived from Website. For most sites
	// this is the registrable domain (eTLD+1); for shared hosts such as
	// wixsite.com it is the full hostname, otherwise every small business on
	// that platform would collapse into one row.
	Domain string

	// NameKey is the fallback dedup key, "normalized name|normalized city",
	// used only when Domain is empty.
	NameKey string

	Email   string
	Phone   string
	Address string
	City    string
	Region  string
	Country string

	// Rating and ReviewCount are nil for CSV-seeded businesses and for any
	// Places result that predates them being requested. They feed the size
	// heuristic; scoring treats their absence as a missing signal, not a zero.
	Rating      *float64
	ReviewCount *int

	PlaceID   string
	Source    Origin
	CreatedAt time.Time
	UpdatedAt time.Time
}
