package service

import "testing"

func TestEffectiveQueueMinutes(t *testing.T) {
	sixty := 60
	cases := []struct {
		name string
		svc  Service
		want int
	}{
		{"falls back to duration", Service{DurationMinutes: 30}, 30},
		{"queue minutes wins", Service{DurationMinutes: 30, QueueMinutes: &sixty}, 60},
	}
	for _, tc := range cases {
		if got := tc.svc.EffectiveQueueMinutes(); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}
