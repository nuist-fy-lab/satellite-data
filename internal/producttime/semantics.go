package producttime

import (
	"strings"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

// Semantic and Subtype describe the data, independently of Kind's scheduling
// contract. WindowMinutes describes a scientific bin, not a scan axis or an
// assertion about archive timestamp anchors.
func applySemantics(p model.Product, profile Profile) Profile {
	code := strings.TrimPrefix(p.ProductKey, p.SourceID+":")
	if p.SourceID == "earthcare" {
		if _, tail, ok := strings.Cut(code, ":"); ok {
			code = tail
		}
	}
	if code == "" {
		code = p.DataType
	}
	set := func(semantic, subtype, label string, window int) {
		profile.Semantic, profile.Subtype, profile.SemanticLabel, profile.WindowMinutes = semantic, subtype, label, window
	}
	if description, ok := remainingScientificCollections[p.ProductKey]; ok && strings.HasPrefix(p.ProductKey, p.SourceID+":") {
		set(description.semantic, description.subtype, description.label, 0)
		return profile
	}
	if p.SourceID == "calipso" && calipsoMonthly[code] {
		profile = Profile{Kind: "monthly", Label: "月产品"}
		set("statistics", "monthly", "月统计", 0)
		return profile
	}
	if p.SourceID == "cloudsat" && (code == "3F-RMCP.R05_V0002" || code == "3S-RMCP.R05_V0002") {
		// The current provider lists file starts without coverage ends. A
		// scientific statistics label must not imply overlap discovery support.
		profile = Profile{Kind: "observation", Label: "数据时间", SelectionBasis: "file_start"}
		set("statistics", "multi_period", "多周期统计", 0)
		return profile
	}
	if p.SourceID == "goes18" || p.SourceID == "goes19" {
		switch code {
		case "ABI-Flood-Day", "ABI-Flood-Day-Shapefiles", "ABI-Flood-Day-TIF":
			profile = Profile{Kind: "daily", Label: "日产品"}
			set("composite", "daily_flood", "日合成", 1440)
			return profile
		case "ABI-Flood-Hourly", "ABI-Flood-Hourly-Shapefiles", "ABI-Flood-Hourly-TIF":
			set("composite", "hourly_flood", "小时合成", 60)
			return profile
		}
	}
	if (p.SourceID == "modis" || p.SourceID == "viirs") && (code == "C3348093425-LAADS" || code == "C3903710663-LAADS") {
		set("composite", "multisensor_half_hour", "半小时合成", 30)
		return profile
	}
	if p.SourceID == "era5" {
		switch code {
		case "reanalysis-era5-pressure-levels", "reanalysis-era5-single-levels", "reanalysis-era5-land":
			set("reanalysis", "hourly", "再分析", 0)
		case "reanalysis-era5-pressure-levels-monthly-means", "reanalysis-era5-single-levels-monthly-means", "reanalysis-era5-land-monthly-means":
			set("reanalysis", "monthly_mean", "月平均再分析", 0)
		}
	}
	if p.SourceID == "mtg" {
		switch code {
		case "EO:EUM:DAT:0801":
			set("event", "fire_alert", "火灾告警", 0)
		case "EO:EUM:DAT:0690", "EO:EUM:DAT:0691", "EO:EUM:DAT:0782":
			set("event", "lightning", "闪电事件", 0)
		case "EO:EUM:DAT:0686", "EO:EUM:DAT:0687", "EO:EUM:DAT:0688":
			set("accumulation", "lightning", "闪电累计", 0)
		case "EO:EUM:DAT:1087":
			set("accumulation", "precipitation", "降水累计", 0)
		}
	}
	if p.SourceID == "earthcare" {
		switch code {
		case "AUX_JSG_1D", "AUX_MET_1D", "AUX_CCOG_B", "MPL_ORBSCT":
			set("ancillary", "support", "辅助资料", 0)
		}
	}
	if p.SourceID == "cloudsat" {
		switch code {
		case "AMSR2-AUX.P_R05", "CRYOSPHERE-AUX.P_R05", "ECMWF-AUX.P1_R05", "ECMWF-AUX.P_R05", "MOD06-1KM-AUX.P1_R05", "MOD06-5KM-AUX.P2_R05", "MODIS-AUX.P_R05", "OCO2CLD-LIDAR-AUX.P_R05":
			set("ancillary", "orbit_support", "辅助资料", 0)
		}
	}
	if p.SourceID == "calipso" {
		switch code {
		case "C3964848342-LARC_CLOUD", "C3964848737-LARC_CLOUD", "C3964849036-LARC_CLOUD", "C3964849107-LARC_CLOUD":
			set("ancillary", "instrument_support", "辅助资料", 0)
		case "C3880519029-LARC_CLOUD", "C3880519095-LARC_CLOUD":
			profile.SemanticLabel = "激光雷达原始数据"
		case "C3880521656-LARC_CLOUD", "C3880521847-LARC_CLOUD", "C3880521958-LARC_CLOUD", "C3880522075-LARC_CLOUD":
			profile.SemanticLabel = "吹雪资料"
		case "C3880703841-LARC_CLOUD", "C3880703949-LARC_CLOUD", "C3880704123-LARC_CLOUD", "C3880704150-LARC_CLOUD", "C3880704173-LARC_CLOUD", "C3880704181-LARC_CLOUD", "C3880704189-LARC_CLOUD", "C4212794150-LARC_CLOUD":
			profile.SemanticLabel = "气溶胶气候资料"
		case "C3880518087-LARC_CLOUD", "C3908938884-LARC_CLOUD":
			profile.SemanticLabel = "红外资料"
		}
	}
	if p.SourceID == "goes16" || p.SourceID == "goes17" || p.SourceID == "goes18" || p.SourceID == "goes19" {
		switch code {
		case "GLM-L2-LCFA":
			set("event", "lightning", "闪电事件", 0)
		case "EXIS-L1b-SFEU", "EXIS-L1b-SFXR":
			set("sensor", "solar_irradiance", "辐照度测量", 0)
		case "MAG-L1b-GEOF":
			set("sensor", "magnetic_field", "磁场测量", 0)
		case "SEIS-L1b-EHIS":
			set("sensor", "particle_integral", "粒子积分测量", 0)
		case "SEIS-L1b-MPSH", "SEIS-L1b-MPSL", "SEIS-L1b-SGPS":
			set("sensor", "particle_flux", "粒子通量序列", 0)
		case "SUVI-L1b-Fe093", "SUVI-L1b-Fe131", "SUVI-L1b-Fe171", "SUVI-L1b-Fe195", "SUVI-L1b-Fe284", "SUVI-L1b-He303":
			set("sensor", "solar_imaging", "太阳极紫外成像", 0)
		}
	}
	if profile.Semantic == "" && profile.SemanticLabel == "" {
		familyKey := p.SourceID + ":" + code
		switch p.SourceID {
		case "goes16", "goes17", "goes18", "goes19":
			familyKey = "goes:" + code
		case "himawari8", "himawari9":
			familyKey = "himawari:" + code
		}
		switch scientificFamilies[familyKey] {
		case "swath":
			set("observation", "swath", "沿轨观测", 0)
		case "orbit":
			set("observation", "orbit", "沿轨观测", 0)
		case "retrieval":
			set("derived", "retrieval", "反演产品", 0)
		case "wind":
			set("derived", "motion_wind", "运动风场", 0)
		case "precipitation":
			set("derived", "precipitation_rate", "降水强度", 0)
		case "multiband":
			set("derived", "multiband_imagery", "多波段影像", 0)
		case "albedo":
			set("derived", "albedo", "反照率", 0)
		case "reprocessed_scan":
			set("scanning", "reprocessed", "扫描时间", 0)
		case "regional_scan":
			set("scanning", "regional", "区域扫描", 0)
		case "regional_sounding":
			set("sounding", "regional", "区域探测", 0)
		}
	}
	return profile
}

// Collection IDs are version-specific; unrelated L3 collections are excluded.
var calipsoMonthly = map[string]bool{
	"C3880523239-LARC_CLOUD": true, "C3964849739-LARC_CLOUD": true,
	"C3880523346-LARC_CLOUD": true, "C4013043981-LARC_CLOUD": true,
	"C3880523461-LARC_CLOUD": true, "C3964849795-LARC_CLOUD": true,
	"C3880523548-LARC_CLOUD": true, "C3880523615-LARC_CLOUD": true, "C3964849308-LARC_CLOUD": true,
	"C3880523630-LARC_CLOUD": true, "C3880523633-LARC_CLOUD": true, "C3964849274-LARC_CLOUD": true,
	"C3880523637-LARC_CLOUD": true, "C3880523642-LARC_CLOUD": true, "C3964849262-LARC_CLOUD": true,
	"C3880523648-LARC_CLOUD": true, "C3880523651-LARC_CLOUD": true, "C3964849249-LARC_CLOUD": true,
	"C3880523659-LARC_CLOUD": true, "C3880523700-LARC_CLOUD": true, "C3964849275-LARC_CLOUD": true,
}
