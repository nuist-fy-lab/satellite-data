package state

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"
)

type ERA5InventoryFile struct {
	Name       string    `json:"name"`
	Dataset    string    `json:"dataset"`
	Bytes      int64     `json:"bytes"`
	Start      string    `json:"start,omitempty"`
	End        string    `json:"end,omitempty"`
	Variables  []string  `json:"variables"`
	Levels     []string  `json:"levels"`
	Times      []string  `json:"times"`
	Area       []float64 `json:"area"`
	Provenance string    `json:"provenance"`
	Verified   bool      `json:"verified"`
	State      string    `json:"state"`
}

type ERA5Inventory struct {
	Files       []ERA5InventoryFile `json:"files"`
	Bytes       int64               `json:"bytes"`
	Unverified  int                 `json:"unverified_files"`
	ScannedAt   string              `json:"scanned_at"`
	GeneratedAt string              `json:"generated_at"`
}

// Inventory requires an indexed local file. Verification additionally requires
// its persisted size and mtime to match. Remote CDS results alone are not stock.
func (s *Store) ERA5Inventory(ctx context.Context) (ERA5Inventory, error) {
	result := ERA5Inventory{Files: make([]ERA5InventoryFile, 0), GeneratedAt: formatTime(time.Now())}
	rows, err := s.db.QueryContext(ctx, `SELECT f.path,f.size,COALESCE(v.official_collection,'era5:unclassified'),
 COALESCE(d.start_ts,p.start_ts,''),COALESCE(d.end_ts,p.end_ts,''),COALESCE(d.inputs_json,p.inputs_json,i.inputs_json,'{}'),f.state,
 CASE WHEN v.source_id='era5' AND f.state='ok' AND f.size=v.size AND f.mtime_ns=v.mtime_ns THEN 1 ELSE 0 END
 FROM local_files f LEFT JOIN verified_archive_files v ON v.path=f.path
 LEFT JOIN local_file_origins o ON o.path=f.path
 LEFT JOIN era5_result_manifest m ON m.downloader_task_id=(SELECT mm.downloader_task_id FROM era5_result_manifest mm
   WHERE mm.collection_id=v.official_collection AND mm.job_id=replace(v.official_item,'era5:','')
   AND mm.asset_id=v.official_asset AND mm.size=f.size ORDER BY mm.downloader_task_id DESC LIMIT 1)
 LEFT JOIN era5_result_slices d ON d.downloader_task_id=m.downloader_task_id
 LEFT JOIN era5_order_parts p ON p.request_id=m.request_id AND p.job_id=m.job_id
 LEFT JOIN era5_order_inputs i ON i.request_id=m.request_id
 WHERE v.source_id='era5' OR o.source_id='era5'
 ORDER BY v.official_collection,f.path`)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var path, encoded string
		file := ERA5InventoryFile{Variables: []string{}, Levels: []string{}, Times: []string{}, Area: []float64{}, Provenance: "verified_file"}
		if err := rows.Scan(&path, &file.Bytes, &file.Dataset, &file.Start, &file.End, &encoded, &file.State, &file.Verified); err != nil {
			return result, err
		}
		if !file.Verified {
			file.Provenance = "scanned_file_unverified"
			result.Unverified++
			file.Dataset = "era5:unclassified"
			file.Start = ""
			file.End = ""
			encoded = "{}"
		}
		file.Name = filepath.Base(path)
		if !strings.HasPrefix(file.Dataset, "era5:") {
			file.Dataset = "era5:" + file.Dataset
		}
		var inputs map[string]json.RawMessage
		if err := json.Unmarshal([]byte(encoded), &inputs); err != nil {
			return result, err
		}
		for key, target := range map[string]*[]string{"variable": &file.Variables, "pressure_level": &file.Levels, "time": &file.Times} {
			if raw := inputs[key]; len(raw) > 0 {
				if json.Unmarshal(raw, target) != nil {
					var value string
					if json.Unmarshal(raw, &value) == nil {
						*target = []string{value}
					}
				}
			}
		}
		if raw := inputs["area"]; len(raw) > 0 {
			_ = json.Unmarshal(raw, &file.Area)
		}
		if file.Verified && file.Start != "" && file.End != "" {
			file.Provenance = "verified_file_and_request_slice"
		}
		result.Bytes += file.Bytes
		result.Files = append(result.Files, file)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT scanned_at FROM local_scan_state WHERE id=1),'')`).Scan(&result.ScannedAt); err != nil {
		return result, err
	}
	return result, nil
}
