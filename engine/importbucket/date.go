package importbucket

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	usDate   = regexp.MustCompile(`^(\d{1,2})/(\d{1,2})/(\d{4})$`)
	year     = regexp.MustCompile(`\b(19|20)\d{2}\b`)
	words    = regexp.MustCompile(`[a-z]+`)
	monthFor = map[string]int{
		"jan": 1, "january": 1, "feb": 2, "february": 2, "mar": 3, "march": 3, "apr": 4, "april": 4,
		"may": 5, "jun": 6, "june": 6, "jul": 7, "july": 7, "aug": 8, "august": 8, "sep": 9, "sept": 9,
		"september": 9, "oct": 10, "october": 10, "nov": 11, "november": 11, "dec": 12, "december": 12,
	}
)

// parseDate reads the loose dates in the old bucket's metadata: "10/5/2025"
// (Nickel Tools' created), "2024", "July 2025", "Circa December 2024" or
// "Circa new year 2023". It returns an ISO partial date, plus a label when
// the text says more than the value does ("Circa 2022" sorts as 2022 but
// shows as written). ok is false when no year can be found.
func parseDate(s string) (value, label string, ok bool) {
	s = strings.TrimSpace(s)
	if m := usDate.FindStringSubmatch(s); m != nil {
		mo, _ := strconv.Atoi(m[1])
		d, _ := strconv.Atoi(m[2])
		if mo < 1 || mo > 12 || d < 1 || d > 31 {
			return "", "", false
		}
		return fmt.Sprintf("%s-%02d-%02d", m[3], mo, d), "", true
	}
	ys := year.FindAllString(s, -1)
	if len(ys) != 1 {
		return "", "", false
	}
	value = ys[0]
	rest := strings.TrimSpace(strings.Replace(s, ys[0], "", 1))
	month := 0
	for _, w := range words.FindAllString(strings.ToLower(rest), -1) {
		if n, isMonth := monthFor[w]; isMonth {
			if month != 0 {
				return "", "", false
			}
			month = n
		}
	}
	if month != 0 {
		value = fmt.Sprintf("%s-%02d", value, month)
	}
	// Plain "2024" or "July 2025" needs no label; anything else keeps its
	// wording.
	if rest != "" && !(month != 0 && monthFor[strings.ToLower(rest)] == month) {
		label = s
		if len(label) > 200 {
			label = label[:200]
		}
	}
	return value, label, true
}
