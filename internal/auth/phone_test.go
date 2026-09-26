package auth

import "testing"

func TestNormalizePhoneNumber(t *testing.T) {
	valid := map[string]string{
		"+992900111222":   "+992900111222",
		"900111222":       "+992900111222",
		"0900111222":      "+992900111222",
		"90 011-12-22":    "+992900111222",
		"992900111222":    "+992900111222",
		"00992900111222":  "+992900111222",
		" (900) 111 222 ": "+992900111222",
		"+15551234567":    "+15551234567",
	}
	for in, want := range valid {
		got, err := NormalizePhoneNumber(in)
		if err != nil || got != want {
			t.Errorf("%q: expected %q, got %q (%v)", in, want, got, err)
		}
	}
	for _, in := range []string{"", "abc", "12", "+", "90011a222", "+99290011122233344455"} {
		if got, err := NormalizePhoneNumber(in); err == nil {
			t.Errorf("%q: expected an error, got %q", in, got)
		}
	}
}
