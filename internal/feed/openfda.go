// Package feed parses recall feeds into domain.Recall values. The only
// format today is the openFDA food enforcement JSON
// (https://api.fda.gov/food/enforcement.json), which is also what the
// FDA's weekly enforcement report publishes.
package feed

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/yashchhabra57/recallroute/internal/domain"
	"github.com/yashchhabra57/recallroute/internal/gtin"
	"github.com/yashchhabra57/recallroute/internal/lotcode"
)

type enforcementEnvelope struct {
	Results []enforcement `json:"results"`
}

type enforcement struct {
	RecallNumber         string `json:"recall_number"`
	EventID              string `json:"event_id"`
	Status               string `json:"status"`
	Classification       string `json:"classification"`
	ProductDescription   string `json:"product_description"`
	CodeInfo             string `json:"code_info"`
	ReasonForRecall      string `json:"reason_for_recall"`
	RecallingFirm        string `json:"recalling_firm"`
	DistributionPattern  string `json:"distribution_pattern"`
	RecallInitiationDate string `json:"recall_initiation_date"`
	ReportDate           string `json:"report_date"`
	OpenFDA              struct {
		UPC []string `json:"upc"`
	} `json:"openfda"`
}

// ParseOpenFDA reads an openFDA enforcement response. Entries without a
// recall number are skipped; malformed dates are left zero.
func ParseOpenFDA(r io.Reader) ([]domain.Recall, error) {
	var env enforcementEnvelope
	dec := json.NewDecoder(r)
	if err := dec.Decode(&env); err != nil {
		return nil, fmt.Errorf("feed: decode openFDA response: %w", err)
	}
	out := make([]domain.Recall, 0, len(env.Results))
	for _, e := range env.Results {
		id := strings.TrimSpace(e.RecallNumber)
		if id == "" {
			continue
		}
		out = append(out, toRecall(e))
	}
	return out, nil
}

func toRecall(e enforcement) domain.Recall {
	rec := domain.Recall{
		ID:             strings.TrimSpace(e.RecallNumber),
		EventID:        e.EventID,
		Firm:           strings.TrimSpace(e.RecallingFirm),
		Product:        strings.TrimSpace(e.ProductDescription),
		Reason:         strings.TrimSpace(e.ReasonForRecall),
		Classification: e.Classification,
		Status:         e.Status,
		Distribution:   e.DistributionPattern,
		CodeInfo:       strings.TrimSpace(e.CodeInfo),
		InitiatedOn:    compactDate(e.RecallInitiationDate),
		ReportedOn:     compactDate(e.ReportDate),
		Source:         "openfda",
		GTINs:          []string{},
	}

	seen := map[string]bool{}
	for _, u := range e.OpenFDA.UPC {
		if n, err := gtin.Normalize(u); err == nil && !seen[n] {
			seen[n] = true
			rec.GTINs = append(rec.GTINs, n)
		}
	}
	// Barcodes are printed in either field depending on who filed it.
	for _, f := range gtin.Extract(rec.Product + "\n" + rec.CodeInfo) {
		if seen[f.GTIN] {
			continue
		}
		seen[f.GTIN] = true
		rec.GTINs = append(rec.GTINs, f.GTIN)
		if f.Inferred {
			rec.GTINsInferred = true
		}
	}
	rec.Spec = lotcode.Parse(rec.CodeInfo)
	return rec
}

// compactDate parses openFDA's YYYYMMDD dates.
func compactDate(s string) time.Time {
	t, err := time.Parse("20060102", strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t
}
