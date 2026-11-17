package match

import (
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/yashchhabra57/recallroute/internal/domain"
	"github.com/yashchhabra57/recallroute/internal/feed"
)

func day(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, _ := time.Parse("2006-01-02", s)
	return t
}

// Mirrors testdata/inventory_sample.csv.
func sampleLots() []domain.Lot {
	mk := func(id int64, name, brand, g, lot, bb string, qty int) domain.Lot {
		return domain.Lot{ID: id, Name: name, Brand: brand, GTIN: g, LotCode: lot, BestBy: day(bb), Quantity: qty}
	}
	return []domain.Lot{
		mk(1, "Cheddar Cheese Curds 12 oz", "Northwoods Creamery", "00071234001019", "2345A", "2026-12-10", 24),
		mk(2, "Cheddar Cheese Curds 12 oz", "Northwoods Creamery", "00071234001019", "2390A", "2027-01-20", 18),
		mk(3, "Cheddar Cheese Curds 12 oz", "Northwoods Creamery", "00071234001019", "", "", 6),
		mk(4, "Peanut Butter Sandwich Crackers 8 pk", "Lakeshore Snacks", "00085777003126", "L5531", "2027-04-01", 60),
		mk(5, "Old Fashioned Rolled Oats 42 oz", "Prairie Gold", "", "P-7781", "2027-03-14", 30),
		mk(6, "Quick Oats 42 oz", "Prairie Gold", "", "P-7790", "2027-06-01", 20),
		mk(7, "Smoked Whitefish Spread 8 oz", "Superior Bay", "00094200007778", "24-123", "2026-12-20", 12),
		mk(8, "Black Beans 15 oz", "Valley Harvest", "00071234009992", "B2201", "2028-05-01", 140),
		mk(9, "Pepper Jack Cheese Curds 12 oz", "Northwoods Creamery", "00071234002054", "2201C", "2026-12-01", 10),
	}
}

func sampleRecalls(t *testing.T) map[string]domain.Recall {
	t.Helper()
	f, err := os.Open("../../testdata/openfda_sample.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rs, err := feed.ParseOpenFDA(f)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]domain.Recall{}
	for _, r := range rs {
		m[r.ID] = r
	}
	return m
}

type hit struct {
	Lot  int64
	Tier domain.Tier
}

func hits(cs []Candidate) []hit {
	out := make([]hit, len(cs))
	for i, c := range cs {
		out[i] = hit{c.LotID, c.Tier}
	}
	return out
}

func TestBarcodePathTiers(t *testing.T) {
	ix := NewIndex(sampleLots())
	rs := sampleRecalls(t)

	// Lot 1: in lot range -> confirmed. Lot 2: outside lot range and
	// best-by window -> cleared. Lot 3: no lot or date on file -> probable.
	got := hits(ix.Match(rs["F-1101-2026"]))
	want := []hit{{1, domain.TierConfirmed}, {3, domain.TierProbable}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("curds: got %v want %v", got, want)
	}

	if got := hits(ix.Match(rs["F-1102-2026"])); !reflect.DeepEqual(got, []hit{{4, domain.TierConfirmed}}) {
		t.Errorf("crackers: got %v", got)
	}
}

func TestInferredBarcodeLowersScore(t *testing.T) {
	ix := NewIndex(sampleLots())
	cs := ix.Match(sampleRecalls(t)["F-1104-2026"])
	if len(cs) != 1 || cs[0].LotID != 7 || cs[0].Tier != domain.TierConfirmed {
		t.Fatalf("got %+v", cs)
	}
	if cs[0].Score != 0.9 {
		t.Errorf("score = %v, want 0.9", cs[0].Score)
	}
}

func TestNamePathOnlyReviewAndRespectsDates(t *testing.T) {
	ix := NewIndex(sampleLots())
	cs := ix.Match(sampleRecalls(t)["F-1103-2026"])
	// Lot 5 matches name and MAR 2027 best-by; lot 6 (Quick Oats) is
	// similar but best-by June 2027 so it is cleared.
	if got := hits(cs); !reflect.DeepEqual(got, []hit{{5, domain.TierReview}}) {
		t.Fatalf("got %v", got)
	}
	if cs[0].Score > 0.6 {
		t.Errorf("review score %v must stay below barcode tiers", cs[0].Score)
	}
}

func TestTerminatedRecallProducesNothing(t *testing.T) {
	ix := NewIndex(sampleLots())
	if cs := ix.Match(sampleRecalls(t)["F-1095-2026"]); len(cs) != 0 {
		t.Errorf("got %+v", cs)
	}
}

func TestNamePathSkipsLotsWithOtherBarcodes(t *testing.T) {
	lots := []domain.Lot{
		{ID: 1, Name: "Cheddar Cheese Curds 12 oz", Brand: "Northwoods Creamery", GTIN: "00099999999999", Quantity: 5},
		{ID: 2, Name: "Cheddar Cheese Curds 12 oz", Brand: "Northwoods Creamery", Quantity: 5},
	}
	r := sampleRecalls(t)["F-1101-2026"]
	got := hits(NewIndex(lots).Match(r))
	// Lot 2 has no barcode and no lot code -> name path, Unknown spec -> review.
	if !reflect.DeepEqual(got, []hit{{2, domain.TierReview}}) {
		t.Errorf("got %v", got)
	}
}

func TestSizeMismatchRulesOut(t *testing.T) {
	lots := []domain.Lot{{ID: 1, Name: "Old Fashioned Rolled Oats 18 oz", Brand: "Prairie Gold", BestBy: day("2027-03-01"), Quantity: 3}}
	if cs := NewIndex(lots).Match(sampleRecalls(t)["F-1103-2026"]); len(cs) != 0 {
		t.Errorf("different package size should not match: %+v", cs)
	}
}

func TestBrandMissingHalvesScore(t *testing.T) {
	recallToks := set(Tokens("Prairie Gold Old Fashioned Rolled Oats, 42 oz canister Prairie Gold Mills"))
	full, _ := nameScore(Tokens("Rolled Oats 42 oz"), Tokens("Prairie Gold"), recallToks, sizes(recallToks))
	half, _ := nameScore(Tokens("Rolled Oats 42 oz"), Tokens("Store Brand"), recallToks, sizes(recallToks))
	if full != 1 || half != 0.5 {
		t.Errorf("full=%v half=%v", full, half)
	}
}

func TestEmptyQuantityNotIndexed(t *testing.T) {
	lots := []domain.Lot{{ID: 1, GTIN: "00071234001019", LotCode: "2345A", Quantity: 0}}
	if cs := NewIndex(lots).Match(sampleRecalls(t)["F-1101-2026"]); len(cs) != 0 {
		t.Errorf("got %+v", cs)
	}
}

func TestTokens(t *testing.T) {
	got := Tokens("Northwoods Creamery Cheddar Cheese Curds, 12 oz bag, UPC 0 71234 00101 9; 16 fl. oz")
	want := []string{"northwood", "creamery", "cheddar", "cheese", "curd", "12oz", "16oz"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func BenchmarkMatch(b *testing.B) {
	// 20k lots: the size of a mid-sized regional food bank's active stock.
	base := sampleLots()
	lots := make([]domain.Lot, 0, 20000)
	for i := 0; i < 20000; i++ {
		l := base[i%len(base)]
		l.ID = int64(i + 1)
		lots = append(lots, l)
	}
	f, _ := os.Open("../../testdata/openfda_sample.json")
	rs, _ := feed.ParseOpenFDA(f)
	f.Close()
	ix := NewIndex(lots)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, r := range rs {
			ix.Match(r)
		}
	}
}
