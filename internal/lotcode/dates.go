package lotcode

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

var months = map[string]time.Month{
	"jan": time.January, "feb": time.February, "mar": time.March, "apr": time.April,
	"may": time.May, "jun": time.June, "jul": time.July, "aug": time.August,
	"sep": time.September, "oct": time.October, "nov": time.November, "dec": time.December,
}

const monthAlt = `(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?`

// One alternation so matches come back in text order. Order inside the
// alternation matters: full dates before month-year.
var dateRe = regexp.MustCompile(`(?i)` +
	`\b(\d{4})-(\d{1,2})-(\d{1,2})\b` + // 1-3: 2026-11-15
	`|\b(\d{1,2})/(\d{1,2})/(\d{2}|\d{4})\b` + // 4-6: 11/15/2026, 11/15/26
	`|\b` + monthAlt + `\s+(\d{1,2}),?\s+(\d{4})\b` + // 7-9: Nov 15, 2026
	`|\b(\d{1,2})\s+` + monthAlt + `\s+(\d{4})\b` + // 10-12: 15 NOV 2026
	`|\b` + monthAlt + `\s+(\d{4})\b`) // 13-14: NOV 2026 (whole month)

type dateTok struct {
	start, end int
	r          DateRange
}

// extractDates finds dates and date ranges in text. It returns the ranges
// and a copy of text with every date replaced by spaces of equal length,
// so callers can keep parsing without tripping over date digits.
func extractDates(text string) ([]DateRange, string) {
	var toks []dateTok
	for _, m := range dateRe.FindAllStringSubmatchIndex(text, -1) {
		g := func(i int) string {
			if m[2*i] < 0 {
				return ""
			}
			return text[m[2*i]:m[2*i+1]]
		}
		var r DateRange
		var ok bool
		switch {
		case g(1) != "":
			r, ok = dayRange(atoi(g(1)), atoi(g(2)), atoi(g(3)))
		case g(4) != "":
			r, ok = dayRange(year(g(6)), atoi(g(4)), atoi(g(5)))
		case g(7) != "":
			r, ok = dayRange(atoi(g(9)), int(months[strings.ToLower(g(7))[:3]]), atoi(g(8)))
		case g(10) != "":
			r, ok = dayRange(atoi(g(12)), int(months[strings.ToLower(g(11))[:3]]), atoi(g(10)))
		case g(13) != "":
			mo := months[strings.ToLower(g(13))[:3]]
			from := time.Date(atoi(g(14)), mo, 1, 0, 0, 0, 0, time.UTC)
			r, ok = DateRange{From: from, To: from.AddDate(0, 1, -1)}, true
		}
		if ok {
			toks = append(toks, dateTok{m[0], m[1], r})
		}
	}

	var out []DateRange
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if i+1 < len(toks) && rangeSepRe.MatchString(text[t.end:toks[i+1].start]) {
			next := toks[i+1]
			if !next.r.To.Before(t.r.From) {
				out = append(out, DateRange{From: t.r.From, To: next.r.To})
				i++
				continue
			}
		}
		out = append(out, t.r)
	}

	b := []byte(text)
	for _, t := range toks {
		for j := t.start; j < t.end; j++ {
			b[j] = ' '
		}
	}
	return out, string(b)
}

func dayRange(y, mo, d int) (DateRange, bool) {
	if mo < 1 || mo > 12 || d < 1 || d > 31 || y < 1900 {
		return DateRange{}, false
	}
	t := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC)
	if t.Day() != d { // e.g. 02/31 rolled into March
		return DateRange{}, false
	}
	return DateRange{From: t, To: t}, true
}

func year(s string) int {
	y := atoi(s)
	if len(s) == 2 {
		y += 2000
	}
	return y
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func day(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
