package reporting

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

var reservedColumns = map[string]bool{"source": true, "metric": true, "value": true, "unit": true}

type Number struct {
	Text string
}

func ParseNumber(value json.Number) (Number, error) {
	text := value.String()
	parsed, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsInf(parsed, 0) || math.IsNaN(parsed) {
		return Number{}, fmt.Errorf("value %q is not a finite binary64 number", text)
	}

	mantissa := text
	if index := strings.IndexAny(mantissa, "eE"); index >= 0 {
		mantissa = mantissa[:index]
	}
	digits := 0
	significantStarted := false
	for _, r := range mantissa {
		if !unicode.IsDigit(r) {
			continue
		}
		if r != '0' {
			significantStarted = true
		}
		if significantStarted {
			digits++
		}
	}
	if digits == 0 {
		digits = 1
	}
	if digits > 17 {
		return Number{}, fmt.Errorf("value %q exceeds 17 significant decimal digits", text)
	}
	return Number{Text: text}, nil
}

type Row struct {
	Source     string
	Dimensions map[string]string
	Metric     string
	Value      Number
	Unit       string
}

func DimensionColumns(rows []Row) []string {
	keys := map[string]bool{}
	for _, row := range rows {
		for key := range row.Dimensions {
			keys[key] = true
		}
	}

	columns := make([]string, 0, len(keys))
	for key := range keys {
		columns = append(columns, key)
	}
	sort.Strings(columns)
	return columns
}

func SortRows(rows []Row) {
	dimensionKeys := DimensionColumns(rows)
	sort.SliceStable(rows, func(i, j int) bool {
		return CompareRows(rows[i], rows[j], dimensionKeys) < 0
	})
}

func CompareRows(left, right Row, dimensionKeys []string) int {
	for _, key := range dimensionKeys {
		if result := strings.Compare(left.Dimensions[key], right.Dimensions[key]); result != 0 {
			return result
		}
	}
	if result := strings.Compare(left.Metric, right.Metric); result != 0 {
		return result
	}
	if result := strings.Compare(left.Unit, right.Unit); result != 0 {
		return result
	}
	return strings.Compare(left.Source, right.Source)
}

func ValidateRows(rows []Row) error {
	dimensionKeys := DimensionColumns(rows)
	SortRows(rows)
	for index := 1; index < len(rows); index++ {
		if CompareRows(rows[index-1], rows[index], dimensionKeys) == 0 {
			return fmt.Errorf("duplicate row for source %q, metric %q, and unit %q", rows[index].Source, rows[index].Metric, rows[index].Unit)
		}
	}
	return nil
}
