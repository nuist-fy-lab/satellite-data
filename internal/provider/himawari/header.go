package himawari

import (
	"encoding/binary"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// parseObservationHeader reads the HSD basic information block. Its end
// timestamp is returned as recorded; its endpoint convention is not inferred.
func parseObservationHeader(header []byte) (time.Time, time.Time, error) {
	if len(header) < 282 || header[0] != 1 {
		return time.Time{}, time.Time{}, fmt.Errorf("incomplete or invalid HSD basic information block")
	}
	var order binary.ByteOrder
	switch header[5] {
	case 0:
		order = binary.LittleEndian
	case 1:
		order = binary.BigEndian
	default:
		return time.Time{}, time.Time{}, fmt.Errorf("invalid HSD byte order")
	}
	if order.Uint16(header[1:3]) != 282 {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid HSD basic information block length")
	}
	convert := func(b []byte) (time.Time, error) {
		mjd := math.Float64frombits(order.Uint64(b))
		if math.IsNaN(mjd) || math.IsInf(mjd, 0) || mjd < 0 || mjd >= 1000000 {
			return time.Time{}, fmt.Errorf("invalid HSD modified Julian date")
		}
		days, fraction := math.Modf(mjd)
		seconds := int64(math.Round(fraction * 86400 * 1e9))
		return time.Date(1858, 11, 17, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(days)).Add(time.Duration(seconds)), nil
	}
	start, err := convert(header[46:54])
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	end, err := convert(header[54:62])
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if !end.After(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid HSD observation interval")
	}
	return start, end, nil
}

func validateObservationIdentity(header []byte, asset string) error {
	if len(header) < 282 {
		return fmt.Errorf("incomplete HSD identity")
	}
	name := filepath.Base(asset)
	match := regionalName.FindStringSubmatch(name)
	if match == nil {
		return fmt.Errorf("invalid HSD object name")
	}
	field := func(b []byte) string { return strings.TrimRight(string(b), "\x00 ") }
	expected := strings.TrimSuffix(name, ".bz2")
	if field(header[114:242]) != expected || field(header[38:42]) != match[3] || field(header[6:22]) != "Himawari-"+name[5:6] {
		return fmt.Errorf("HSD header identity does not match object %s", name)
	}
	var order binary.ByteOrder = binary.LittleEndian
	if header[5] == 1 {
		order = binary.BigEndian
	}
	timeline, err := strconv.Atoi(match[2])
	if err != nil || int(order.Uint16(header[44:46])) != timeline {
		return fmt.Errorf("HSD timeline does not match object %s", name)
	}
	return nil
}
