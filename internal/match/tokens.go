package match

import (
	"strings"
	"unicode"
)

var stopwords = map[string]bool{
	"a": true, "an": true, "and": true, "the": true, "of": true, "with": true,
	"in": true, "for": true, "by": true, "inc": true, "llc": true, "co": true,
	"company": true, "corp": true, "pack": true, "pk": true, "bag": true,
	"jar": true, "tub": true, "can": true, "canister": true, "box": true,
	"ct": true, "count": true, "fl": true, "upc": true, "upcs": true, "net": true, "wt": true,
}

var units = map[string]string{
	"oz": "oz", "ounce": "oz", "ounces": "oz",
	"lb": "lb", "lbs": "lb", "pound": "lb", "pounds": "lb",
	"g": "g", "gram": "g", "grams": "g", "kg": "kg",
	"ml": "ml", "l": "l", "liter": "l", "gal": "gal", "gallon": "gal",
	"fl": "", // "fl oz": drop "fl", keep "oz"
}

// Tokens lower-cases text, splits on non-alphanumerics, joins a number
// with the unit that follows it ("12 oz" -> "12oz"), drops stopwords and
// bare numbers, and trims a plural "s".
func Tokens(text string) []string {
	raw := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '.'
	})
	var out []string
	seen := map[string]bool{}
	add := func(t string) {
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	for i := 0; i < len(raw); i++ {
		t := strings.Trim(raw[i], ".")
		if t == "" {
			continue
		}
		if isNumber(t) {
			// Look past "fl" for the unit.
			j := i + 1
			if j < len(raw) && strings.Trim(raw[j], ".") == "fl" {
				j++
			}
			if j < len(raw) {
				if u, ok := units[strings.Trim(raw[j], ".")]; ok && u != "" {
					add(t + u)
					i = j
				}
			}
			continue // bare numbers are noise (UPC digits, pack counts)
		}
		// "12oz" written together is already a size token.
		if isSize(t) {
			add(t)
			continue
		}
		if stopwords[t] || len(t) < 2 || hasDigit(t) {
			continue
		}
		add(stem(t))
	}
	return out
}

func stem(t string) string {
	if len(t) > 3 && strings.HasSuffix(t, "s") && !strings.HasSuffix(t, "ss") {
		return t[:len(t)-1]
	}
	return t
}

// isSize reports whether t is a number glued to a unit, e.g. "12oz".
func isSize(t string) bool {
	i := 0
	for i < len(t) && (t[i] >= '0' && t[i] <= '9' || t[i] == '.') {
		i++
	}
	if i == 0 || i == len(t) {
		return false
	}
	u, ok := units[t[i:]]
	return ok && u == t[i:]
}

func isNumber(t string) bool {
	for _, c := range t {
		if (c < '0' || c > '9') && c != '.' {
			return false
		}
	}
	return t != ""
}

func hasDigit(t string) bool { return strings.ContainsAny(t, "0123456789") }
