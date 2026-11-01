package producttime

import "strings"

// These GOES-16 collection keys were checked against actual NOAA GCS objects
// at the indicated UTC dates (2026-11-01 catalogue audit, 808 identities).
// They are observations, not claims about an exact M3-to-M6 changeover date.
// Search/matching continues to read each granule's scan_mode metadata.
var goes16SampledModeCollections = map[string]bool{
	"ABI-L1b-RadC": true, "ABI-L1b-RadC-Reproc": true,
	"ABI-L1b-RadF": true, "ABI-L1b-RadF-Reproc": true,
	"ABI-L1b-RadM": true, "ABI-L2-ACMC": true,
	"ABI-L2-ACTPC": true, "ABI-L2-ACTPF": true,
	"ABI-L2-AODC": true, "ABI-L2-CMIPC": true,
	"ABI-L2-CMIPF": true, "ABI-L2-CMIPM": true,
	"ABI-L2-CODC": true, "ABI-L2-FDCC": true,
	"ABI-L2-FDCF": true, "ABI-L2-LSTC": true,
	"ABI-L2-LSTF": true, "ABI-L2-MCMIPC": true,
	"ABI-L2-MCMIPF": true, "ABI-L2-MCMIPM": true,
}

func observedScanModes(key string) []ScanModeSample {
	code, ok := strings.CutPrefix(key, "goes16:")
	if !ok || !goes16SampledModeCollections[code] {
		return nil
	}
	return []ScanModeSample{
		{Date: "2018-06-15", Mode: "M3"},
		{Date: "2020-06-15", Mode: "M6"},
		{Date: "2023-06-15", Mode: "M6"},
	}
}
