package qrcode

import "testing"

func TestQRCode_Code(t *testing.T) {
	cases := []struct {
		seq  int64
		want string
	}{
		{1, "QW-0001"},
		{31, "QW-0031"},
		{10000, "QW-10000"},
	}
	for _, c := range cases {
		got := QRCode{Seq: c.seq}.Code()
		if got != c.want {
			t.Errorf("Code() for seq=%d = %q, want %q", c.seq, got, c.want)
		}
	}
}

func TestParseCode(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"QW-0031", 31, false},
		{"qw-0031", 31, false},
		{"QW0031", 31, false},
		{"  QW-31  ", 31, false},
		{"31", 31, false},
		{"not-a-code", 0, true},
		{"", 0, true},
	}
	for _, c := range cases {
		got, err := ParseCode(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseCode(%q) expected an error, got %d", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseCode(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseCode(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestGenerateToken(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 200; i++ {
		tok, err := generateToken()
		if err != nil {
			t.Fatalf("generateToken() error: %v", err)
		}
		if tok == "" {
			t.Fatal("generateToken() returned empty string")
		}
		if seen[tok] {
			t.Fatalf("generateToken() produced a duplicate: %q", tok)
		}
		seen[tok] = true
	}
}
