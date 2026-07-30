package metaads

// archiveResponse is the ads_archive envelope. Only the fields the request asks
// for are modelled.
type archiveResponse struct {
	Data   []ad `json:"data"`
	Paging struct {
		Next string `json:"next"`
	} `json:"paging"`
}

// ad is one entry in the Ad Library.
//
// PageName is the only identity the archive offers: there is no website field
// to match a business on, which is the root of this source's unreliability.
type ad struct {
	ID                  string `json:"id"`
	PageName            string `json:"page_name"`
	AdDeliveryStartTime string `json:"ad_delivery_start_time"`
	AdSnapshotURL       string `json:"ad_snapshot_url"`
}
