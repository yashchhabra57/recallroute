package inventory

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseSample(t *testing.T) {
	f, err := os.Open("../../testdata/inventory_sample.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	lots, errs, err := ParseCSV(f)
	if err != nil || len(errs) != 0 {
		t.Fatalf("err=%v rowErrs=%v", err, errs)
	}
	if len(lots) != 9 {
		t.Fatalf("got %d lots", len(lots))
	}
	first := lots[0]
	if first.GTIN != "00071234001019" || first.LotCode != "2345A" || first.Quantity != 24 ||
		!first.BestBy.Equal(time.Date(2026, 12, 10, 0, 0, 0, 0, time.UTC)) || first.Location != "Cooler B" {
		t.Errorf("first = %+v", first)
	}
	if lots[2].LotCode != "" || !lots[2].BestBy.IsZero() {
		t.Errorf("blank lot/date row = %+v", lots[2])
	}
}

func TestAliasesAndRowErrors(t *testing.T) {
	in := utf8BOM + "Item #,Description,UPC,Lot Number,Exp,Qty,Bin\n" +
		"A1,Rice 2 lb,036000291452,R1,3/7/2027,\"1,200\",D4\n" +
		"A2,Bad barcode,036000291453,R2,,5,D4\n" +
		"A3,Bad qty,,R3,,-2,D4\n" +
		"A4,Bad date,,R4,next tuesday,1,D4\n" +
		",,,,,,\n" +
		"A5,,,R5,,1,D4\n"
	lots, errs, err := ParseCSV(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(lots) != 1 || lots[0].SKU != "A1" || lots[0].Quantity != 1200 || lots[0].GTIN != "00036000291452" {
		t.Fatalf("lots = %+v", lots)
	}
	wantLines := []int{3, 4, 5, 7}
	if len(errs) != len(wantLines) {
		t.Fatalf("errs = %+v", errs)
	}
	for i, e := range errs {
		if e.Line != wantLines[i] || e.Message == "" {
			t.Errorf("err %d = %+v, want line %d", i, e, wantLines[i])
		}
	}
}

func TestMissingRequiredColumn(t *testing.T) {
	if _, _, err := ParseCSV(strings.NewReader("sku,name\nA,B\n")); err == nil {
		t.Error("expected error for missing quantity column")
	}
	if _, _, err := ParseCSV(strings.NewReader("")); err == nil {
		t.Error("expected error for empty file")
	}
}
