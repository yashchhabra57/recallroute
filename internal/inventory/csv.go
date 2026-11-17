// Package inventory imports stock lists exported from warehouse systems
// or kept in spreadsheets.
package inventory

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/yashchhabra57/recallroute/internal/domain"
	"github.com/yashchhabra57/recallroute/internal/gtin"
)

// RowError describes one rejected row. Line is 1-based and counts the header.
type RowError struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// Header aliases seen in real exports, mapped to canonical names.
var aliases = map[string]string{
	"sku": "sku", "item": "sku", "item_number": "sku", "item_no": "sku",
	"name": "name", "description": "name", "product": "name", "item_description": "name",
	"brand": "brand", "manufacturer": "brand",
	"gtin": "gtin", "upc": "gtin", "barcode": "gtin", "ean": "gtin",
	"lot": "lot_code", "lot_code": "lot_code", "lot_number": "lot_code", "lot_no": "lot_code",
	"best_by": "best_by", "bestby": "best_by", "best_before": "best_by", "exp": "best_by",
	"expiration": "best_by", "expiration_date": "best_by", "use_by": "best_by",
	"quantity": "quantity", "qty": "quantity", "on_hand": "quantity", "cases": "quantity",
	"location": "location", "bin": "location", "slot": "location",
}

// Excel's "CSV UTF-8" export starts the file with a byte order mark.
const utf8BOM = "\xef\xbb\xbf"

var dateLayouts = []string{"2006-01-02", "1/2/2006", "01/02/2006", "1/2/06", "01/02/06"}

// ParseCSV reads lots from CSV. Bad rows are reported and skipped so one
// typo does not block a 5,000-row import; a missing required column fails
// the whole file.
func ParseCSV(r io.Reader) ([]domain.Lot, []RowError, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true

	header, err := cr.Read()
	if err != nil {
		return nil, nil, fmt.Errorf("inventory: read header: %w", err)
	}
	col := map[string]int{}
	for i, h := range header {
		key := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, utf8BOM)))
		key = strings.Trim(strings.NewReplacer(" ", "_", "-", "_", "#", "").Replace(key), "_")
		if canon, ok := aliases[key]; ok {
			if _, dup := col[canon]; !dup {
				col[canon] = i
			}
		}
	}
	for _, req := range []string{"name", "quantity"} {
		if _, ok := col[req]; !ok {
			return nil, nil, fmt.Errorf("inventory: missing required column %q", req)
		}
	}

	var lots []domain.Lot
	var rowErrs []RowError
	line := 1
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		line++
		if err != nil {
			rowErrs = append(rowErrs, RowError{line, err.Error()})
			continue
		}
		get := func(name string) string {
			i, ok := col[name]
			if !ok || i >= len(rec) {
				return ""
			}
			return strings.TrimSpace(rec[i])
		}
		if strings.Join(rec, "") == "" {
			continue // blank line
		}
		l, msg := parseRow(get)
		if msg != "" {
			rowErrs = append(rowErrs, RowError{line, msg})
			continue
		}
		lots = append(lots, l)
	}
	return lots, rowErrs, nil
}

func parseRow(get func(string) string) (domain.Lot, string) {
	l := domain.Lot{
		SKU:      get("sku"),
		Name:     get("name"),
		Brand:    get("brand"),
		LotCode:  get("lot_code"),
		Location: get("location"),
	}
	if l.Name == "" {
		return l, "name is empty"
	}
	q, err := strconv.Atoi(strings.ReplaceAll(get("quantity"), ",", ""))
	if err != nil || q < 0 {
		return l, fmt.Sprintf("quantity %q is not a whole number >= 0", get("quantity"))
	}
	l.Quantity = q
	if g := get("gtin"); g != "" {
		n, err := gtin.Normalize(g)
		if err != nil {
			return l, fmt.Sprintf("barcode %q has the wrong length or check digit", g)
		}
		l.GTIN = n
	}
	if bb := get("best_by"); bb != "" {
		t, ok := parseDate(bb)
		if !ok {
			return l, fmt.Sprintf("best-by date %q not recognized (use YYYY-MM-DD)", bb)
		}
		l.BestBy = t
	}
	return l, ""
}

func parseDate(s string) (time.Time, bool) {
	for _, layout := range dateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
