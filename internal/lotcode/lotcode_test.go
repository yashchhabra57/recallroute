package lotcode

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func d(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"lot# 24-a100": "24A100",
		" 2341a ":      "2341A",
		"LOT 7/22/B":   "722B",
		"":             "",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseExactList(t *testing.T) {
	s := Parse("Lot codes: 2341A, 2342A and 2350B")
	want := []string{"2341A", "2342A", "2350B"}
	if !reflect.DeepEqual(s.Exact, want) {
		t.Fatalf("Exact = %v, want %v", s.Exact, want)
	}
	if len(s.Ranges) != 0 || s.All {
		t.Errorf("unexpected spec %+v", s)
	}
}

func TestParseWordRange(t *testing.T) {
	s := Parse("Lot codes 2341A through 2349A")
	want := []Range{{Prefix: "", Lo: 2341, Hi: 2349, Suffix: "A"}}
	if !reflect.DeepEqual(s.Ranges, want) {
		t.Fatalf("Ranges = %+v", s.Ranges)
	}
	for lot, v := range map[string]Verdict{"2345A": Match, "2349a": Match, "2350A": NoMatch, "2345B": NoMatch} {
		if got := s.Test(lot, time.Time{}).Verdict; got != v {
			t.Errorf("Test(%s) = %v, want %v", lot, got, v)
		}
	}
}

func TestParseHyphenRangeWithPrefix(t *testing.T) {
	s := Parse("Lot 24A100-24A150")
	want := []Range{{Prefix: "24A", Lo: 100, Hi: 150}}
	if !reflect.DeepEqual(s.Ranges, want) {
		t.Fatalf("Ranges = %+v", s.Ranges)
	}
	if s.Test("24A120", time.Time{}).Verdict != Match {
		t.Error("24A120 should match")
	}
	if s.Test("25A120", time.Time{}).Verdict != NoMatch {
		t.Error("25A120 should not match (different prefix)")
	}
}

func TestHyphenWithUnequalWidthsIsACode(t *testing.T) {
	s := Parse("Lot # 24-123")
	if len(s.Ranges) != 0 || !reflect.DeepEqual(s.Exact, []string{"24123"}) {
		t.Fatalf("got %+v", s)
	}
}

func TestParseAllLots(t *testing.T) {
	for _, text := range []string{"All lots", "all lot codes within expiry", "Recall includes all best by dates"} {
		s := Parse(text)
		if !s.All {
			t.Errorf("Parse(%q).All = false", text)
		}
		if s.Test("anything", time.Time{}).Verdict != Match {
			t.Errorf("%q: all-lots spec should match", text)
		}
	}
}

func TestParsePrefix(t *testing.T) {
	s := Parse("All products with lot numbers beginning with 'K24'")
	// "All products" is not "all lots": the prefix should constrain it.
	if s.All || !reflect.DeepEqual(s.Prefixes, []string{"K24"}) {
		t.Fatalf("got %+v", s)
	}
	if s.Test("k24-0091", time.Time{}).Verdict != Match || s.Test("K25-0091", time.Time{}).Verdict != NoMatch {
		t.Error("prefix test wrong")
	}
}

func TestParseBestByRange(t *testing.T) {
	s := Parse("Best By dates 11/01/2026 through 11/30/2026")
	want := []DateRange{{From: d("2026-11-01"), To: d("2026-11-30")}}
	if !reflect.DeepEqual(s.BestBy, want) {
		t.Fatalf("BestBy = %+v", s.BestBy)
	}
	if s.HasLotCriteria() {
		t.Errorf("dates must not be read as lot codes: %+v", s)
	}
	cases := map[string]Verdict{"2026-11-15": Match, "2026-11-30": Match, "2026-12-01": NoMatch, "": Unknown}
	for bb, want := range cases {
		var tt time.Time
		if bb != "" {
			tt = d(bb)
		}
		if got := s.Test("ZZ999", tt).Verdict; got != want {
			t.Errorf("best-by %q: got %v want %v", bb, got, want)
		}
	}
}

func TestDateFormats(t *testing.T) {
	cases := map[string]DateRange{
		"Use by 2026-03-07":               {d("2026-03-07"), d("2026-03-07")},
		"Best if used by 3/7/26":          {d("2026-03-07"), d("2026-03-07")},
		"BEST BY MAR 7, 2026":             {d("2026-03-07"), d("2026-03-07")},
		"best before 07 March 2026":       {d("2026-03-07"), d("2026-03-07")},
		"EXP FEB 2028":                    {d("2028-02-01"), d("2028-02-29")},
		"Sell by 2026-03-01 - 2026-03-09": {d("2026-03-01"), d("2026-03-09")},
	}
	for text, want := range cases {
		s := Parse(text)
		if len(s.BestBy) != 1 || !reflect.DeepEqual(s.BestBy[0], want) {
			t.Errorf("Parse(%q).BestBy = %+v, want %+v", text, s.BestBy, want)
		}
	}
}

func TestDatesWithoutBestByKeywordIgnored(t *testing.T) {
	s := Parse("Manufactured 10/02/2026, lot 7781")
	if len(s.BestBy) != 0 || !reflect.DeepEqual(s.Exact, []string{"7781"}) {
		t.Fatalf("got %+v", s)
	}
}

func TestInvalidDateSkipped(t *testing.T) {
	if s := Parse("best by 02/31/2026"); len(s.BestBy) != 0 {
		t.Errorf("Feb 31 should be rejected, got %+v", s.BestBy)
	}
}

func TestMixedLotsAndDatesIsSafetyBiasedOR(t *testing.T) {
	s := Parse("Lot 2341A; Best By 11/12/2026, UPC 0 36000 29145 2")
	if !reflect.DeepEqual(s.Exact, []string{"2341A"}) || len(s.BestBy) != 1 {
		t.Fatalf("got %+v", s)
	}
	// UPC digits must not leak into lot codes.
	if len(s.Exact) != 1 {
		t.Errorf("exact = %v", s.Exact)
	}
	if s.Test("9999", d("2026-11-12")).Verdict != Match {
		t.Error("matching best-by alone should flag the lot")
	}
	if s.Test("2341A", d("2027-01-01")).Verdict != Match {
		t.Error("matching lot alone should flag the lot")
	}
	if s.Test("9999", d("2027-01-01")).Verdict != NoMatch {
		t.Error("neither matches -> no match")
	}
}

func TestUnparseableIsUnknown(t *testing.T) {
	s := Parse("Product distributed through retail stores in Wisconsin")
	if !s.Empty() {
		t.Fatalf("expected empty spec, got %+v", s)
	}
	if r := s.Test("2341A", time.Time{}); r.Verdict != Unknown || r.Reason == "" {
		t.Errorf("got %+v", r)
	}
}

func TestMissingLotOnInventoryIsUnknown(t *testing.T) {
	s := Parse("Lot 2341A")
	if v := s.Test("", time.Time{}).Verdict; v != Unknown {
		t.Errorf("got %v", v)
	}
}

func TestSizesNotLotCodes(t *testing.T) {
	s := Parse("Lots 2341A 16oz")
	if !reflect.DeepEqual(s.Exact, []string{"2341A"}) {
		t.Errorf("got %v", s.Exact)
	}
}

// Code-info strings below are taken from live openFDA food enforcement
// reports (Sept-Nov 2026) that the first version of the parser missed.
func TestLiveFeedShapes(t *testing.T) {
	t.Run("no codes means all units", func(t *testing.T) {
		for _, text := range []string{"No codes", "none", "no lot code", "N/A", "No code applied.", "All batch codes."} {
			if s := Parse(text); !s.All {
				t.Errorf("Parse(%q) = %+v, want All", text, s)
			}
		}
	})
	t.Run("no lot code but a best-by date is date-only", func(t *testing.T) {
		s := Parse("No lot code, Best By JUL 22 27")
		want := []DateRange{{d("2027-07-22"), d("2027-07-22")}}
		if s.All || !reflect.DeepEqual(s.BestBy, want) {
			t.Errorf("got %+v", s)
		}
	})
	t.Run("unlabelled range", func(t *testing.T) {
		s := Parse("190A26184 - 190A26216")
		if !reflect.DeepEqual(s.Ranges, []Range{{Prefix: "190A", Lo: 26184, Hi: 26216}}) {
			t.Errorf("got %+v", s)
		}
	})
	t.Run("unlabelled codes with dates and states", func(t *testing.T) {
		s := Parse("Illinois: TFIC211, 8/7/2026. Texas: TFTX209U11, 8/7/2026; TFTX211, 8/9/2026.")
		want := []string{"TFIC211", "TFTX209U11", "TFTX211"}
		if !reflect.DeepEqual(s.Exact, want) || len(s.BestBy) != 0 {
			t.Errorf("got %+v", s)
		}
	})
	t.Run("codes interleaved with dates", func(t *testing.T) {
		s := Parse("L6080C SEP 20 2027 L6079C SEP 19 2027")
		if !reflect.DeepEqual(s.Exact, []string{"L6079C", "L6080C"}) {
			t.Errorf("got %+v", s)
		}
	})
	t.Run("long state-by-state list", func(t *testing.T) {
		text := strings.Repeat("Florida: TFFL208, 8/9/2026; TFFL210, 8/10/2026. ", 4) + "Texas: TFTX208016, 8/8/2026; TFTX209016, 8/9/2026; TFTX210016, 8/10/2026."
		if s := Parse(text); len(s.Exact) != 5 {
			t.Errorf("got %+v", s.Exact)
		}
	})
	t.Run("hyphenated single codes", func(t *testing.T) {
		if s := Parse("108-728-420"); !reflect.DeepEqual(s.Exact, []string{"108728420"}) {
			t.Errorf("got %+v", s)
		}
	})
	t.Run("long prose is not mined", func(t *testing.T) {
		long := strings.Repeat("Products were distributed to 120 retail stores in 14 states. ", 6)
		if s := Parse(long); !s.Empty() {
			t.Errorf("got %+v", s)
		}
	})
}
