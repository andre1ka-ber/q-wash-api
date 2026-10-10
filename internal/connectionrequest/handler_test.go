package connectionrequest

import "testing"

func TestValidPhone(t *testing.T) {
	cases := map[string]bool{
		"+992 900 12 34 56":  true,
		"+7 (999) 123-45-67": true,
		"900123456":          true,
		"12345678":           false, // 8 digits
		"1234567890123456":   false, // 16 digits
		"+992abc900123456":   false,
		"":                   false,
	}
	for in, want := range cases {
		if got := validPhone(in); got != want {
			t.Errorf("validPhone(%q) = %v, want %v", in, got, want)
		}
	}
}
