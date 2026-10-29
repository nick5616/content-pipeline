package importbucket

import "testing"

func TestParseDate(t *testing.T) {
	for _, c := range []struct{ in, value, label string }{
		// Every form found in gs://artandmedia.
		{"10/5/2025", "2025-10-05", ""},
		{"11/30/2025", "2025-11-30", ""},
		{"Circa 2022", "2022", "Circa 2022"},
		{"Circa December 2024", "2024-12", "Circa December 2024"},
		{"Circa new year 2023", "2023", "Circa new year 2023"},
		{"July 2025", "2025-07", ""},
		{"2024", "2024", ""},
		{"Sometime in like Oct 2025", "2025-10", "Sometime in like Oct 2025"},
		{" Sept 2021 ", "2021-09", ""},
	} {
		v, l, ok := parseDate(c.in)
		if !ok || v != c.value || l != c.label {
			t.Errorf("parseDate(%q) = %q, %q, %v; want %q, %q", c.in, v, l, ok, c.value, c.label)
		}
	}
	for _, bad := range []string{"3", "", "someday", "13/1/2025", "2022 or 2023", "May or June 2024"} {
		if v, l, ok := parseDate(bad); ok {
			t.Errorf("parseDate(%q) = %q, %q; want not ok", bad, v, l)
		}
	}
}
