package producttime

var iasiScientificFamilies = map[string]string{
	"iasi:EO:EUM:DAT:METOP:IASIL1C-ALL": "orbit",
	"iasi:EO:EUM:DAT:METOP:IASPCS01":    "orbit",
	"iasi:EO:EUM:DAT:METOP:IASIL2COX":   "retrieval",
	"iasi:EO:EUM:DAT:METOP:IASSND02":    "retrieval",
	"iasi:EO:EUM:DAT:0959":              "retrieval",
	"iasi:EO:EUM:DAT:0960":              "retrieval",
	"iasi:EO:EUM:DAT:0576":              "retrieval",
	"iasi:EO:EUM:DAT:0758":              "orbit",
	"iasi:EO:EUM:DAT:1027":              "retrieval",
	"iasi:EO:EUM:DAT:0590":              "orbit",
}

var iasiNGScientificFamilies = map[string]string{
	"iasi-ng:EO:EUM:DAT:0442": "swath",
}

func scientificFamily(key string) string {
	if family := scientificFamilies[key]; family != "" {
		return family
	}
	if family := iasiScientificFamilies[key]; family != "" {
		return family
	}
	return iasiNGScientificFamilies[key]
}
