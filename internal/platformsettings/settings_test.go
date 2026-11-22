package platformsettings

import "testing"

func ptr[T any](v T) *T { return &v }

func TestUpdateValidate(t *testing.T) {
	cases := []struct {
		name string
		u    Update
		ok   bool
	}{
		{"empty is valid here (the handler rejects it separately)", Update{}, true},
		{"horizon 1..60", Update{BookingHorizonDays: ptr(60)}, true},
		{"horizon 0", Update{BookingHorizonDays: ptr(0)}, false},
		{"horizon 61", Update{BookingHorizonDays: ptr(61)}, false},
		{"no-show off", Update{NoShowAutoCancelMin: ptr(0)}, true},
		{"no-show 240", Update{NoShowAutoCancelMin: ptr(240)}, true},
		{"no-show negative", Update{NoShowAutoCancelMin: ptr(-1)}, false},
		{"no-show 241", Update{NoShowAutoCancelMin: ptr(241)}, false},
		{"cutoff 1440", Update{ClientCancelCutoffMin: ptr(1440)}, true},
		{"cutoff 1441", Update{ClientCancelCutoffMin: ptr(1441)}, false},
		{"toggles need no range", Update{SMSConfirmation: ptr(true), NewPointModeration: ptr(false)}, true},
	}
	for _, c := range cases {
		if err := c.u.Validate(); (err == nil) != c.ok {
			t.Errorf("%s: err=%v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestUpdateEmpty(t *testing.T) {
	if !(Update{}).empty() || (Update{SMSConfirmation: ptr(false)}).empty() {
		t.Error("empty() must be true only when no field is set (false is still a value)")
	}
}

func TestDefaultsKeepOldBehavior(t *testing.T) {
	d := Defaults()
	if d.NoShowAutoCancelMin != 0 || d.ClientCancelCutoffMin != 0 || d.SMSConfirmation || !d.NewPointModeration || d.BookingHorizonDays != 7 {
		t.Errorf("unexpected defaults: %+v", d)
	}
}
