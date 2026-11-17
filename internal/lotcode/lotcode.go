// Package lotcode turns the free-text "code info" of a recall notice
// ("Lot codes 2341A through 2349A; Best By 11/01/2026 - 11/30/2026")
// into a Spec that can be tested against a single inventory lot.
//
// Matching is deliberately safety-biased: when a notice lists both lot
// codes and best-by dates, a lot that satisfies EITHER criterion is
// flagged. A false positive costs a volunteer five minutes of checking;
// a false negative puts a recalled product in a family's grocery bag.
package lotcode

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Range is a run of lot codes that share a prefix and suffix and differ
// only in one numeric field, e.g. 24A100..24A150B.
type Range struct {
	Prefix string `json:"prefix"`
	Lo     int    `json:"lo"`
	Hi     int    `json:"hi"`
	Suffix string `json:"suffix"`
}

// DateRange is an inclusive span of best-by dates (day precision, UTC).
type DateRange struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// Spec is the parsed form of a recall's code info.
type Spec struct {
	All      bool        `json:"all,omitempty"`
	Exact    []string    `json:"exact,omitempty"`
	Ranges   []Range     `json:"ranges,omitempty"`
	Prefixes []string    `json:"prefixes,omitempty"`
	BestBy   []DateRange `json:"best_by,omitempty"`
}

// Empty reports whether nothing usable was parsed.
func (s Spec) Empty() bool {
	return !s.All && len(s.Exact) == 0 && len(s.Ranges) == 0 && len(s.Prefixes) == 0 && len(s.BestBy) == 0
}

// HasLotCriteria reports whether the spec constrains lot codes (as opposed
// to only best-by dates).
func (s Spec) HasLotCriteria() bool {
	return len(s.Exact) > 0 || len(s.Ranges) > 0 || len(s.Prefixes) > 0
}

// Verdict is the outcome of testing a lot against a Spec.
type Verdict int

const (
	// Unknown means the spec could not decide (nothing parsed, or only
	// dates were given and the lot has no best-by date). A person must look.
	Unknown Verdict = iota
	// Match means the lot is covered by the recall.
	Match
	// NoMatch means the lot is definitely outside the recall.
	NoMatch
)

func (v Verdict) String() string {
	switch v {
	case Match:
		return "match"
	case NoMatch:
		return "no_match"
	default:
		return "unknown"
	}
}

// Result explains a verdict in words a volunteer can check.
type Result struct {
	Verdict Verdict
	Reason  string
}

// Test checks one inventory lot. bestBy may be the zero time if unknown.
func (s Spec) Test(lot string, bestBy time.Time) Result {
	if s.All {
		return Result{Match, "recall covers all lots"}
	}
	if s.Empty() {
		return Result{Unknown, "recall code info could not be parsed"}
	}
	n := Normalize(lot)
	if s.HasLotCriteria() && n != "" {
		for _, e := range s.Exact {
			if n == e {
				return Result{Match, fmt.Sprintf("lot %s listed in recall", e)}
			}
		}
		for _, r := range s.Ranges {
			if r.Contains(n) {
				return Result{Match, fmt.Sprintf("lot %s within range %s", n, r)}
			}
		}
		for _, p := range s.Prefixes {
			if strings.HasPrefix(n, p) {
				return Result{Match, fmt.Sprintf("lot %s begins with %s", n, p)}
			}
		}
	}
	if len(s.BestBy) > 0 {
		if !bestBy.IsZero() {
			d := day(bestBy)
			for _, r := range s.BestBy {
				if !d.Before(r.From) && !d.After(r.To) {
					return Result{Match, fmt.Sprintf("best-by %s within %s", d.Format("2006-01-02"), r)}
				}
			}
		} else if !s.HasLotCriteria() || n == "" {
			return Result{Unknown, "recall is defined by best-by date and this lot has none recorded"}
		}
	}
	if s.HasLotCriteria() && n == "" && len(s.BestBy) == 0 {
		return Result{Unknown, "recall lists lot codes and this lot has no code recorded"}
	}
	return Result{NoMatch, "lot code and best-by date outside recall"}
}

// Contains reports whether normalized lot code n falls in the range.
func (r Range) Contains(n string) bool {
	m := codeShape.FindStringSubmatch(n)
	if m == nil || m[1] != r.Prefix || m[3] != r.Suffix {
		return false
	}
	v, err := strconv.Atoi(m[2])
	return err == nil && v >= r.Lo && v <= r.Hi
}

func (r Range) String() string {
	return fmt.Sprintf("%s%d%s-%s%d%s", r.Prefix, r.Lo, r.Suffix, r.Prefix, r.Hi, r.Suffix)
}

func (d DateRange) String() string {
	if d.From.Equal(d.To) {
		return d.From.Format("2006-01-02")
	}
	return d.From.Format("2006-01-02") + ".." + d.To.Format("2006-01-02")
}

// Normalize upper-cases a lot code and drops separators and a leading
// "LOT" label, so "lot# 24-a100" and "24A100" compare equal.
func Normalize(code string) string {
	u := strings.ToUpper(strings.TrimSpace(code))
	u = strings.TrimPrefix(u, "LOT")
	var b strings.Builder
	for _, c := range u {
		if (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		}
	}
	return b.String()
}

var (
	// prefix (anything, non-greedy) + last digit run + trailing letters.
	codeShape = regexp.MustCompile(`^(.*?)(\d+)([A-Z]*)$`)

	allRe = regexp.MustCompile(`(?i)\ball\s+(?:lot(?:\s+codes?|\s+numbers?|s)?|codes|product\s+codes|best[\s-]+by\s+dates|expiration\s+dates|dates)\b`)

	// Start of a lot list. The capture is the list text up to the next
	// sentence break or the next unrelated label.
	lotListRe = regexp.MustCompile(`(?i)\b(?:lot|batch|product)?\s*(?:lot\s+codes?|lot\s+numbers?|lot\s*#|lots?|codes?|batch(?:es)?)\b\s*(?:#|no\.?|numbers?)?\s*[:#]?\s*(.+?)(?:\.\s|;|\.$|$|\b(?:best|use|sell|enjoy|exp|expiration|upc|gtin|packed|manufactured|net\s+wt)\b)`)

	prefixRe = regexp.MustCompile(`(?i)\b(?:begin(?:ning|s)?|start(?:ing|s)?)\s+with\s+(?:the\s+)?(?:letters?\s+|digits?\s+|numbers?\s+)?["']?([A-Za-z0-9]+)`)

	bestByKeywordRe = regexp.MustCompile(`(?i)\b(?:best\s+(?:if\s+used\s+)?by|best\s+before|use\s+by|sell\s+by|enjoy\s+by|exp(?:iration|iry)?\.?(?:\s+dates?)?|bb)\b`)

	rangeSepRe = regexp.MustCompile(`(?i)^\s*(?:through|thru|to|until|-|–|—)\s*$`)
	listSepRe  = regexp.MustCompile(`(?i)\s*(?:,|&|\band\b|\bor\b|/|\s{2,})\s*`)
	listRngRe  = regexp.MustCompile(`(?i)^(.+?)\s*(?:\s(?:through|thru|to)\s|\s*[-–—]\s*)\s*(.+)$`)
)

// Parse reads a recall's code info into a Spec. Unrecognized text is
// ignored; check Spec.Empty to see whether anything usable was found.
func Parse(text string) Spec {
	var s Spec
	if strings.TrimSpace(text) == "" {
		return s
	}
	if allRe.MatchString(text) {
		s.All = true
		return s
	}

	// Dates first, then blank them out so "11/01/2026" is not read as a
	// lot code and the hyphens in ISO dates are not read as ranges.
	dates, rest := extractDates(text)
	if len(dates) > 0 && bestByKeywordRe.MatchString(text) {
		s.BestBy = dates
	}

	for _, m := range prefixRe.FindAllStringSubmatch(rest, -1) {
		s.Prefixes = appendUnique(s.Prefixes, Normalize(m[1]))
	}
	rest = prefixRe.ReplaceAllString(rest, " ")

	for _, m := range lotListRe.FindAllStringSubmatch(rest, -1) {
		parseLotList(m[1], &s)
	}
	sort.Strings(s.Exact)
	return s
}

func parseLotList(list string, s *Spec) {
	// Ranges can contain separators that also split lists ("A - B"), so
	// split on list separators that are not hyphens first.
	for _, item := range listSepRe.Split(list, -1) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if m := listRngRe.FindStringSubmatch(item); m != nil {
			wordSep := regexp.MustCompile(`(?i)\s(?:through|thru|to)\s`).MatchString(item)
			if r, ok := makeRange(m[1], m[2], wordSep); ok {
				s.Ranges = append(s.Ranges, r)
				continue
			}
		}
		for _, tok := range strings.Fields(item) {
			n := Normalize(tok)
			if isLotCode(n) {
				s.Exact = appendUnique(s.Exact, n)
			}
		}
	}
}

// makeRange builds a Range from two endpoints. A bare hyphen is only
// treated as a range when both sides have the same number of digits,
// because hyphens also appear inside real lot codes ("24-123").
func makeRange(a, b string, wordSep bool) (Range, bool) {
	na, nb := Normalize(a), Normalize(b)
	ma, mb := codeShape.FindStringSubmatch(na), codeShape.FindStringSubmatch(nb)
	if ma == nil || mb == nil || !isLotCode(na) || !isLotCode(nb) {
		return Range{}, false
	}
	// "2341A through 49A" style shorthand is not supported; endpoints must
	// share prefix and suffix.
	if ma[1] != mb[1] || ma[3] != mb[3] {
		return Range{}, false
	}
	if !wordSep && len(ma[2]) != len(mb[2]) {
		return Range{}, false
	}
	lo, err1 := strconv.Atoi(ma[2])
	hi, err2 := strconv.Atoi(mb[2])
	if err1 != nil || err2 != nil || lo >= hi {
		return Range{}, false
	}
	return Range{Prefix: ma[1], Lo: lo, Hi: hi, Suffix: ma[3]}, true
}

// isLotCode filters out words like "CODES" and sizes like "16OZ": a lot
// code must contain a digit and be at least 3 characters.
func isLotCode(n string) bool {
	if len(n) < 3 {
		return false
	}
	if strings.HasSuffix(n, "OZ") || strings.HasSuffix(n, "LB") || strings.HasSuffix(n, "LBS") {
		return false
	}
	return strings.ContainsAny(n, "0123456789")
}

func appendUnique(xs []string, x string) []string {
	if x == "" {
		return xs
	}
	for _, y := range xs {
		if y == x {
			return xs
		}
	}
	return append(xs, x)
}
