// Package match decides which inventory lots a recall covers.
//
// Two paths:
//
//  1. Barcode path. The recall lists GTINs; lots with the same GTIN are
//     looked up in O(1) and then tested against the recall's lot/date spec.
//  2. Name path. Lots with no barcode on file (common for donated stock),
//     or recalls that print no barcode, are compared by product-name
//     tokens and brand. These only ever produce "review" alerts.
//
// An inverted token index keeps the name path from comparing every
// recall with every lot.
package match

import (
	"fmt"
	"sort"

	"github.com/yashchhabra57/recallroute/internal/domain"
	"github.com/yashchhabra57/recallroute/internal/lotcode"
)

// ReviewThreshold is the minimum name score for a review alert.
const ReviewThreshold = 0.6

// Candidate is a proposed alert.
type Candidate struct {
	RecallID string
	LotID    int64
	Tier     domain.Tier
	Score    float64
	Reasons  []string
}

// Index is a read-only view of inventory built for matching.
type Index struct {
	lots    []domain.Lot
	byGTIN  map[string][]int
	byToken map[string][]int
	tokens  [][]string // name tokens per lot
	brands  [][]string // brand tokens per lot
}

// NewIndex indexes lots. Lots with zero quantity are skipped: there is
// nothing on the shelf to pull.
func NewIndex(lots []domain.Lot) *Index {
	ix := &Index{byGTIN: map[string][]int{}, byToken: map[string][]int{}}
	for _, l := range lots {
		if l.Quantity <= 0 {
			continue
		}
		i := len(ix.lots)
		ix.lots = append(ix.lots, l)
		if l.GTIN != "" {
			ix.byGTIN[l.GTIN] = append(ix.byGTIN[l.GTIN], i)
		}
		toks := Tokens(l.Name)
		ix.tokens = append(ix.tokens, toks)
		ix.brands = append(ix.brands, Tokens(l.Brand))
		for _, t := range toks {
			if !isSize(t) {
				ix.byToken[t] = append(ix.byToken[t], i)
			}
		}
	}
	return ix
}

// Match returns candidates for one recall, best first.
func (ix *Index) Match(r domain.Recall) []Candidate {
	if !r.Active() {
		return nil
	}
	var out []Candidate
	matched := map[int]bool{}

	for _, g := range r.GTINs {
		for _, i := range ix.byGTIN[g] {
			if matched[i] {
				continue
			}
			l := ix.lots[i]
			res := r.Spec.Test(l.LotCode, l.BestBy)
			if res.Verdict == lotcode.NoMatch {
				continue
			}
			matched[i] = true
			c := Candidate{RecallID: r.ID, LotID: l.ID, Reasons: []string{fmt.Sprintf("barcode %s matches", g)}}
			if res.Verdict == lotcode.Match {
				c.Tier, c.Score = domain.TierConfirmed, 1.0
			} else {
				c.Tier, c.Score = domain.TierProbable, 0.75
			}
			if r.GTINsInferred {
				c.Score -= 0.1
				c.Reasons = append(c.Reasons, "recall printed the barcode without its check digit")
			}
			c.Reasons = append(c.Reasons, res.Reason)
			out = append(out, c)
		}
	}

	out = append(out, ix.nameMatches(r, len(r.GTINs) > 0, matched)...)

	sort.SliceStable(out, func(a, b int) bool { return out[a].Score > out[b].Score })
	return out
}

func (ix *Index) nameMatches(r domain.Recall, recallHasGTIN bool, skip map[int]bool) []Candidate {
	recallToks := set(Tokens(r.Product + " " + r.Firm))
	recallSizes := sizes(recallToks)

	// Count shared tokens per lot using the inverted index.
	shared := map[int]int{}
	for t := range recallToks {
		for _, i := range ix.byToken[t] {
			shared[i]++
		}
	}

	var out []Candidate
	for i := range shared {
		if skip[i] {
			continue
		}
		l := ix.lots[i]
		// A lot with a barcode that the recall does not list is a
		// different product, however similar the name.
		if recallHasGTIN && l.GTIN != "" {
			continue
		}
		score, why := nameScore(ix.tokens[i], ix.brands[i], recallToks, recallSizes)
		if score < ReviewThreshold {
			continue
		}
		res := r.Spec.Test(l.LotCode, l.BestBy)
		if res.Verdict == lotcode.NoMatch {
			continue
		}
		out = append(out, Candidate{
			RecallID: r.ID,
			LotID:    l.ID,
			Tier:     domain.TierReview,
			Score:    round2(score * 0.6), // never outranks a barcode match
			Reasons:  append(why, res.Reason),
		})
	}
	return out
}

// nameScore is the share of the lot's name tokens that appear in the
// recall text, halved when the brand does not appear. Mismatched package
// sizes (12oz vs 24oz) rule the lot out.
func nameScore(lotToks, brandToks []string, recallToks map[string]bool, recallSizes map[string]bool) (float64, []string) {
	if len(lotToks) == 0 {
		return 0, nil
	}
	hit := 0
	for _, t := range lotToks {
		if isSize(t) && len(recallSizes) > 0 && !recallSizes[t] {
			return 0, nil
		}
		if recallToks[t] {
			hit++
		}
	}
	score := float64(hit) / float64(len(lotToks))
	brandOK := len(brandToks) > 0
	for _, t := range brandToks {
		if !recallToks[t] {
			brandOK = false
			break
		}
	}
	why := []string{fmt.Sprintf("product name similarity %.2f", score)}
	if brandOK {
		why = append(why, "brand appears in recall")
	} else {
		score /= 2
		why = append(why, "brand not found in recall")
	}
	return score, why
}

func set(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func sizes(toks map[string]bool) map[string]bool {
	m := map[string]bool{}
	for t := range toks {
		if isSize(t) {
			m[t] = true
		}
	}
	return m
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}
