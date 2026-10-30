package producttime

// These exact snapshot collections describe scientific content, not cadence.
// Sources: https://portal.maap.eo.esa.int/earthcare/earthcare-data-offer/
// https://ladsweb.modaps.eosdis.nasa.gov/missions-and-measurements/products/MODFNSS
// https://user.eumetsat.int/resources/user-guides/mtg-fci-l2-fir-data-guide
// https://www.ospo.noaa.gov/data/messages/2023/12/MSG_20231215_0000.html
type scientificDescription struct {
	semantic string
	subtype  string
	label    string
}

var remainingScientificCollections = map[string]scientificDescription{
	"earthcare:EarthCAREL1InstChecked_MAAP:ATL_CSC_1B": {"ancillary", "calibration", "校准资料"},
	"earthcare:EarthCAREL1InstChecked_MAAP:ATL_DCC_1B": {"ancillary", "calibration", "校准资料"},
	"earthcare:EarthCAREL1InstChecked_MAAP:ATL_FSC_1B": {"ancillary", "calibration", "校准资料"},
	"earthcare:EarthCAREL1InstChecked_MAAP:BBR_LIN_1B": {"ancillary", "calibration", "校准资料"},
	"earthcare:EarthCAREL1InstChecked_MAAP:BBR_SOL_1B": {"ancillary", "calibration", "校准资料"},
	"earthcare:EarthCAREL1InstChecked_MAAP:MSI_BBS_1B": {"ancillary", "calibration", "校准资料"},
	"earthcare:EarthCAREL1InstChecked_MAAP:MSI_DRK_1B": {"ancillary", "calibration", "校准资料"},
	"earthcare:EarthCAREL1InstChecked_MAAP:MSI_SD1_1B": {"ancillary", "calibration", "校准资料"},
	"earthcare:EarthCAREL1InstChecked_MAAP:MSI_SD2_1B": {"ancillary", "calibration", "校准资料"},
	"earthcare:EarthCAREL1InstChecked_MAAP:MSI_TRF_1B": {"ancillary", "calibration", "校准资料"},
	"himawari8:AHI-L2-FLDK-Clouds":                     {"derived", "cloud", "云产品"},
	"himawari8:AHI-L2-FLDK-RainfallRate":               {"derived", "precipitation_rate", "降水强度"},
	"himawari8:AHI-L2-FLDK-SST":                        {"derived", "sea_surface_temperature", "海表温度"},
	"himawari8:AHI-L2-FLDK-Winds":                      {"derived", "motion_wind", "大气运动风"},
	"himawari9:AHI-L2-FLDK-Clouds":                     {"derived", "cloud", "云产品"},
	"himawari9:AHI-L2-FLDK-Winds":                      {"derived", "motion_wind", "大气运动风"},
	"modis:C1444214556-LAADS":                          {"observation", "station_subset", "站点观测子集"},
	"modis:C1444310549-LAADS":                          {"observation", "station_subset", "站点观测子集"},
	"modis:C1444318098-LAADS":                          {"observation", "station_subset", "站点观测子集"},
	"modis:C1444624388-LAADS":                          {"observation", "station_subset", "站点观测子集"},
	"modis:C1444771617-LAADS":                          {"observation", "station_subset", "站点观测子集"},
	"modis:C1444780364-LAADS":                          {"observation", "station_subset", "站点观测子集"},
	"mtg:EO:EUM:DAT:0676":                              {"derived", "motion_wind", "大气运动风"},
	"mtg:EO:EUM:DAT:0677":                              {"derived", "all_sky_radiation", "全天空辐射"},
	"mtg:EO:EUM:DAT:0678":                              {"derived", "cloud_mask", "云掩膜"},
	"mtg:EO:EUM:DAT:0679":                              {"derived", "clear_sky_reflectance", "晴空反射率"},
	"mtg:EO:EUM:DAT:0680":                              {"derived", "cloud_type", "云类型"},
	"mtg:EO:EUM:DAT:0681":                              {"derived", "cloud_top", "云顶温度和高度"},
	"mtg:EO:EUM:DAT:0682":                              {"derived", "fire_monitoring", "火灾监测"},
	"mtg:EO:EUM:DAT:0683":                              {"derived", "instability", "大气不稳定指数"},
	"mtg:EO:EUM:DAT:0684":                              {"derived", "cloud_analysis", "云分析"},
	"mtg:EO:EUM:DAT:0685":                              {"derived", "outgoing_longwave", "出射长波辐射"},
	"mtg:EO:EUM:DAT:0694":                              {"derived", "sea_surface_temperature", "海表温度"},
	"mtg:EO:EUM:DAT:0799":                              {"derived", "all_sky_radiation", "全天空辐射"},
	"mtg:EO:EUM:DAT:0800":                              {"derived", "cloud_mask", "云掩膜"},
	"mtg:EO:EUM:DAT:0998":                              {"derived", "motion_wind", "大气运动风"},
	"mtg:EO:EUM:DAT:1086":                              {"derived", "precipitation_rate", "降水强度"},
	"mtg:EO:EUM:DAT:1088":                              {"derived", "land_surface_temperature", "陆表温度"},
	"mtg:EO:EUM:DAT:1091":                              {"derived", "snow_detection", "积雪检测"},
}
