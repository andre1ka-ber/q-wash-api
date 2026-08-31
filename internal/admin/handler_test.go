package admin

import "testing"

func TestParseHHMM_Valid(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"00:00", 0},
		{"08:00", 480},
		{"09:30", 570},
		{"23:59", 1439},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := parseHHMM(c.in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("parseHHMM(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

func TestParseHHMM_Invalid(t *testing.T) {
	cases := []string{"", "not-a-time", "0900", "12:99", "9:00:00"}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			if _, err := parseHHMM(in); err == nil {
				t.Errorf("expected an error for %q, got none", in)
			}
		})
	}
}
