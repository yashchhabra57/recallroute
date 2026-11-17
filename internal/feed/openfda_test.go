package feed

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseOpenFDASample(t *testing.T) {
	f, err := os.Open("../../testdata/openfda_sample.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	recalls, err := ParseOpenFDA(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(recalls) != 5 {
		t.Fatalf("got %d recalls, want 5 (entry without recall number skipped)", len(recalls))
	}

	byID := map[string]int{}
	for i, r := range recalls {
		byID[r.ID] = i
	}

	curds := recalls[byID["F-1101-2026"]]
	if !reflect.DeepEqual(curds.GTINs, []string{"00071234001019"}) || curds.GTINsInferred {
		t.Errorf("curds GTINs = %v inferred=%v", curds.GTINs, curds.GTINsInferred)
	}
	if len(curds.Spec.Ranges) != 1 || len(curds.Spec.BestBy) != 1 {
		t.Errorf("curds spec = %+v", curds.Spec)
	}
	if !curds.InitiatedOn.Equal(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("initiated = %v", curds.InitiatedOn)
	}
	if curds.Classification != "Class I" || curds.Firm != "Northwoods Creamery LLC" {
		t.Errorf("curds = %+v", curds)
	}

	crackers := recalls[byID["F-1102-2026"]]
	if len(crackers.GTINs) != 2 || !crackers.Spec.All {
		t.Errorf("crackers = %v %+v", crackers.GTINs, crackers.Spec)
	}

	oats := recalls[byID["F-1103-2026"]]
	if len(oats.GTINs) != 0 || len(oats.Spec.BestBy) != 1 {
		t.Errorf("oats = %v %+v", oats.GTINs, oats.Spec)
	}

	fish := recalls[byID["F-1104-2026"]]
	if !fish.GTINsInferred || !reflect.DeepEqual(fish.GTINs, []string{"00094200007778"}) {
		t.Errorf("fish GTINs = %v inferred=%v", fish.GTINs, fish.GTINsInferred)
	}
	if !reflect.DeepEqual(fish.Spec.Exact, []string{"24123", "24124"}) {
		t.Errorf("fish lots = %v", fish.Spec.Exact)
	}

	if recalls[byID["F-1095-2026"]].Active() {
		t.Error("terminated recall should be inactive")
	}
}

func TestParseOpenFDAPrefersOpenFDAUPCField(t *testing.T) {
	body := `{"results":[{"recall_number":"F-1","product_description":"Thing","code_info":"","openfda":{"upc":["036000291452","bad"]}}]}`
	rs, err := ParseOpenFDA(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rs[0].GTINs, []string{"00036000291452"}) {
		t.Errorf("GTINs = %v", rs[0].GTINs)
	}
}

func TestParseOpenFDABadJSON(t *testing.T) {
	if _, err := ParseOpenFDA(strings.NewReader("{not json")); err == nil {
		t.Error("expected error")
	}
}
