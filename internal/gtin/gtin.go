// Package gtin normalizes and validates retail barcodes (UPC-A, EAN-8,
// EAN-13, GTIN-14) and pulls them out of free-text recall notices.
//
// Every code is stored as a 14-digit GTIN so that "0 41220 12345 6",
// "041220123456" and "00041220123456" all compare equal.
package gtin

import (
	"errors"
	"regexp"
	"strings"
)

// ErrInvalid is returned when a code has the wrong length or check digit.
var ErrInvalid = errors.New("gtin: invalid code")

// CheckDigit computes the GS1 mod-10 check digit for body, which must be
// the code's digits without its final check digit.
//
// Weights alternate 3,1,3,1... starting from the rightmost body digit.
func CheckDigit(body string) (byte, error) {
	if body == "" {
		return 0, ErrInvalid
	}
	sum := 0
	weight := 3
	for i := len(body) - 1; i >= 0; i-- {
		c := body[i]
		if c < '0' || c > '9' {
			return 0, ErrInvalid
		}
		sum += int(c-'0') * weight
		weight = 4 - weight // toggles 3 <-> 1
	}
	return byte('0' + (10-sum%10)%10), nil
}

// Valid reports whether digits (no separators) is a well-formed GTIN-8/12/13/14.
func Valid(digits string) bool {
	switch len(digits) {
	case 8, 12, 13, 14:
	default:
		return false
	}
	want, err := CheckDigit(digits[:len(digits)-1])
	return err == nil && want == digits[len(digits)-1]
}

// Normalize strips spaces and hyphens, validates the code and returns it
// left-padded to 14 digits.
func Normalize(code string) (string, error) {
	d := stripSeparators(code)
	if !Valid(d) {
		return "", ErrInvalid
	}
	return strings.Repeat("0", 14-len(d)) + d, nil
}

// Found is a code pulled from text.
type Found struct {
	GTIN string // 14-digit normalized code
	Raw  string // the text it came from
	// Inferred is true when the notice printed only 11 digits (a UPC-A
	// without its check digit, which recall notices often do) and the
	// check digit was computed here.
	Inferred bool
}

var (
	// A keyword followed by a run of digits that may contain single spaces
	// or hyphens, e.g. "UPC: 0 41220 12345 6" or "UPC# 041220-123456".
	keywordRe = regexp.MustCompile(`(?i)\b(?:UPC|GTIN|EAN|barcode)s?\b[\s#:.\-]*(?:code|number|no\.?)?[\s#:.\-]*((?:\d[ \-]?){7,13}\d)`)
	// Bare 12-14 digit runs with no keyword. Only kept if the check digit is
	// valid, which rules out most phone numbers and lot numbers.
	bareRe = regexp.MustCompile(`\b\d{12,14}\b`)
	// Continuation codes in a list after a keyword, e.g. "UPCs 0123..., 0456...".
	listItemRe = regexp.MustCompile(`^[\s,;&]*(?:and\s+)?((?:\d[ \-]?){7,13}\d)`)
)

// Extract finds every distinct barcode mentioned in text, in order of
// first appearance.
func Extract(text string) []Found {
	var out []Found
	seen := map[string]bool{}
	add := func(raw string, keyword bool) {
		d := stripSeparators(raw)
		f, ok := interpret(d, keyword)
		if !ok || seen[f.GTIN] {
			return
		}
		f.Raw = strings.TrimSpace(raw)
		seen[f.GTIN] = true
		out = append(out, f)
	}

	for _, loc := range keywordRe.FindAllStringSubmatchIndex(text, -1) {
		add(text[loc[2]:loc[3]], true)
		// Follow a comma-separated list after the first code.
		rest := text[loc[1]:]
		for {
			m := listItemRe.FindStringSubmatchIndex(rest)
			if m == nil {
				break
			}
			add(rest[m[2]:m[3]], true)
			rest = rest[m[1]:]
		}
	}
	for _, raw := range bareRe.FindAllString(text, -1) {
		add(raw, false)
	}
	return out
}

func interpret(d string, keyword bool) (Found, bool) {
	if Valid(d) {
		n, _ := Normalize(d)
		return Found{GTIN: n}, true
	}
	// 11 digits right after "UPC": try a missing leading zero first
	// (UPC-A printed without its number-system digit is rare), then a
	// missing check digit (common).
	if keyword && len(d) == 11 {
		if Valid("0" + d) {
			n, _ := Normalize("0" + d)
			return Found{GTIN: n, Inferred: true}, true
		}
		c, err := CheckDigit(d)
		if err == nil {
			n, _ := Normalize(d + string(c))
			return Found{GTIN: n, Inferred: true}, true
		}
	}
	return Found{}, false
}

func stripSeparators(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '-' {
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}
