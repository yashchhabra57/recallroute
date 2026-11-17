package gtin

import (
	"reflect"
	"testing"
)

func TestCheckDigit(t *testing.T) {
	cases := map[string]byte{
		"03600029145":  '2', // UPC-A textbook example 036000291452
		"400638133393": '1', // EAN-13 4006381333931
		"9638507":      '4', // EAN-8 96385074
	}
	for body, want := range cases {
		got, err := CheckDigit(body)
		if err != nil || got != want {
			t.Errorf("CheckDigit(%q) = %q, %v; want %q", body, got, err, want)
		}
	}
	if _, err := CheckDigit("12a4"); err == nil {
		t.Error("expected error for non-digit body")
	}
	if _, err := CheckDigit(""); err == nil {
		t.Error("expected error for empty body")
	}
}

func TestValid(t *testing.T) {
	valid := []string{"036000291452", "4006381333931", "96385074", "00036000291452"}
	for _, c := range valid {
		if !Valid(c) {
			t.Errorf("Valid(%q) = false, want true", c)
		}
	}
	invalid := []string{"036000291453", "12345", "0360002914520000", "03600029145x"}
	for _, c := range invalid {
		if Valid(c) {
			t.Errorf("Valid(%q) = true, want false", c)
		}
	}
}

func TestNormalizeEquivalentForms(t *testing.T) {
	forms := []string{"036000291452", "0 36000 29145 2", "036000-291452", "00036000291452"}
	for _, f := range forms {
		got, err := Normalize(f)
		if err != nil {
			t.Fatalf("Normalize(%q): %v", f, err)
		}
		if got != "00036000291452" {
			t.Errorf("Normalize(%q) = %s", f, got)
		}
	}
	if _, err := Normalize("036000291453"); err != ErrInvalid {
		t.Errorf("bad check digit: err = %v, want ErrInvalid", err)
	}
}

func gtins(fs []Found) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.GTIN
	}
	return out
}

func TestExtractKeywordWithSpaces(t *testing.T) {
	text := "Brand X Peanut Butter, 16 oz jar, UPC 0 36000 29145 2, sold nationwide"
	got := Extract(text)
	if len(got) != 1 || got[0].GTIN != "00036000291452" || got[0].Inferred {
		t.Fatalf("got %+v", got)
	}
	if got[0].Raw != "0 36000 29145 2" {
		t.Errorf("Raw = %q", got[0].Raw)
	}
}

func TestExtractList(t *testing.T) {
	text := "UPCs: 036000291452, 4006381333931 and 96385074. Distributed in WI."
	want := []string{"00036000291452", "04006381333931", "00000096385074"}
	if got := gtins(Extract(text)); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestExtractInfersMissingCheckDigit(t *testing.T) {
	got := Extract("Frozen peas 12 oz UPC 03600029145")
	if len(got) != 1 || got[0].GTIN != "00036000291452" || !got[0].Inferred {
		t.Fatalf("got %+v", got)
	}
}

func TestExtractBareCodeNeedsValidCheckDigit(t *testing.T) {
	// 036000291452 is valid; 555123456780 is not a valid GTIN.
	got := gtins(Extract("Call 555123456780 about item 036000291452"))
	if !reflect.DeepEqual(got, []string{"00036000291452"}) {
		t.Errorf("got %v", got)
	}
}

func TestExtractDeduplicates(t *testing.T) {
	got := Extract("UPC 036000291452 ... UPC 0 36000 29145 2 ... 036000291452")
	if len(got) != 1 {
		t.Errorf("want 1 unique code, got %+v", got)
	}
}

func TestExtractNothing(t *testing.T) {
	if got := Extract("Lot 2341A best by 11/12/2026"); len(got) != 0 {
		t.Errorf("got %+v", got)
	}
}
