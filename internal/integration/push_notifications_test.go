//go:build integration

package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"q-wash-api/internal/app"
	"q-wash-api/internal/user"
)

func TestDevices_RegisterMoveAndUnregister(t *testing.T) {
	env := newTestEnv(t)
	alice := env.loginAs(t, uniquePhone(400), "")
	bob := env.loginAs(t, uniquePhone(401), "")

	register := func(access, token, platform string) apiResponse {
		return env.do(t, http.MethodPut, "/api/v1/me/devices", access, map[string]any{"token": token, "platform": platform})
	}
	countTokens := func(token string) (n int64) {
		env.db.Raw("SELECT count(*) FROM device_tokens WHERE token = ?", token).Scan(&n)
		return n
	}
	ownerOf := func(token string) (id string) {
		env.db.Raw("SELECT user_id::text FROM device_tokens WHERE token = ?", token).Scan(&id)
		return id
	}

	if r := env.do(t, http.MethodPut, "/api/v1/me/devices", "", map[string]any{"token": "x", "platform": "ios"}); r.status != http.StatusUnauthorized {
		t.Fatalf("anonymous: expected 401, got %d", r.status)
	}

	t.Run("validation", func(t *testing.T) {
		if r := register(alice, "  ", "android"); r.status != http.StatusBadRequest || errCode(r) != "invalid_token" {
			t.Errorf("blank token: %d (%v)", r.status, r.body)
		}
		if r := register(alice, strings.Repeat("t", 4097), "android"); r.status != http.StatusBadRequest || errCode(r) != "invalid_token" {
			t.Errorf("oversized token: %d (%v)", r.status, r.body)
		}
		if r := register(alice, "tok", "windows"); r.status != http.StatusBadRequest || errCode(r) != "invalid_platform" {
			t.Errorf("bad platform: %d (%v)", r.status, r.body)
		}
	})

	t.Run("registering is idempotent", func(t *testing.T) {
		for i := 0; i < 2; i++ {
			if r := register(alice, "tok-A", "android"); r.status != http.StatusNoContent {
				t.Fatalf("expected 204, got %d (%v)", r.status, r.body)
			}
		}
		if n := countTokens("tok-A"); n != 1 {
			t.Fatalf("expected one row, got %d", n)
		}
	})

	t.Run("a token signing in under another account moves to it", func(t *testing.T) {
		aliceID := ownerOf("tok-A")
		if r := register(bob, "tok-A", "ios"); r.status != http.StatusNoContent {
			t.Fatalf("expected 204, got %d", r.status)
		}
		if n := countTokens("tok-A"); n != 1 || ownerOf("tok-A") == aliceID {
			t.Fatalf("token must be reassigned, not duplicated (rows=%d)", n)
		}
	})

	t.Run("unregister only removes the caller's own token", func(t *testing.T) {
		if r := env.do(t, http.MethodDelete, "/api/v1/me/devices/tok-A", alice, nil); r.status != http.StatusNoContent {
			t.Fatalf("expected 204, got %d", r.status)
		}
		if countTokens("tok-A") != 1 {
			t.Fatal("alice must not be able to remove bob's token")
		}
		if r := env.do(t, http.MethodDelete, "/api/v1/me/devices/tok-A", bob, nil); r.status != http.StatusNoContent {
			t.Fatalf("expected 204, got %d", r.status)
		}
		if countTokens("tok-A") != 0 {
			t.Fatal("bob's own token should be gone")
		}
	})
}

type pushFixture struct {
	bookingFixture
	customerAccess string
	customerPhone  string
}

func (e *testEnv) setUpPushFixture(t *testing.T, seed int64) pushFixture {
	t.Helper()
	staffPhone := uniquePhone(seed)
	e.loginAs(t, staffPhone, user.RoleStaff)
	adminAccess := e.loginAs(t, uniquePhone(seed+1), user.RoleAdmin)
	customerPhone := uniquePhone(seed + 2)
	customerAccess := e.loginAs(t, customerPhone, "")
	return pushFixture{
		bookingFixture: e.setUpBookingFixture(t, adminAccess, staffPhone),
		customerAccess: customerAccess,
		customerPhone:  customerPhone,
	}
}

func (e *testEnv) registerDevice(t *testing.T, access, token string) {
	t.Helper()
	if r := e.do(t, http.MethodPut, "/api/v1/me/devices", access, map[string]any{"token": token, "platform": "android"}); r.status != http.StatusNoContent {
		t.Fatalf("register device: %d (%v)", r.status, r.body)
	}
}

// book creates a booking for the fixture's customer.
func (e *testEnv) book(t *testing.T, fx pushFixture, daysAhead int) (string, time.Time) {
	t.Helper()
	return e.bookAs(t, fx, fx.customerAccess, daysAhead)
}

// bookAs creates a booking for any customer and returns (id, scheduled start).
func (e *testEnv) bookAs(t *testing.T, fx pushFixture, access string, daysAhead int) (string, time.Time) {
	t.Helper()
	carID := e.createCar(t, access, "Car")
	r := e.do(t, http.MethodPost, "/api/v1/queue", access, map[string]any{
		"car_id": carID, "service_id": fx.serviceID, "box_number": 1,
		"price_option_id": fx.priceOptionID, "scheduled_start_at": futureBookingTime(daysAhead),
	})
	if r.status != http.StatusCreated {
		t.Fatalf("booking: %d (%v)", r.status, r.body)
	}
	start, err := time.Parse(time.RFC3339, r.str("scheduled_start_at"))
	if err != nil {
		t.Fatal(err)
	}
	return r.str("id"), start
}

func (e *testEnv) setStatus(t *testing.T, fx pushFixture, id, status string) {
	t.Helper()
	if r := e.do(t, http.MethodPatch, "/api/v1/queue/"+id+"/status", fx.staffAccess, map[string]any{"status": status}); r.status != http.StatusOK {
		t.Fatalf("status -> %s: %d (%v)", status, r.status, r.body)
	}
}

func TestPush_StartedAndFinishedOnStatusChange(t *testing.T) {
	env := newTestEnv(t)
	fx := env.setUpPushFixture(t, 410)
	env.registerDevice(t, fx.customerAccess, "tok-live")
	id, _ := env.book(t, fx, 2)

	env.setStatus(t, fx, id, "waiting")
	time.Sleep(200 * time.Millisecond)
	if got := env.push.snapshot(); len(got) != 0 {
		t.Fatalf("arriving is not a notified stage, got %+v", got)
	}

	env.setStatus(t, fx, id, "washing")
	got := env.push.waitForPush(t, 1)
	if got[0].Token != "tok-live" || got[0].Msg.Title != "Мойка началась" ||
		got[0].Msg.Data["queue_id"] != id || got[0].Msg.Data["kind"] != "started" || got[0].Msg.Data["type"] != "booking_stage" {
		t.Fatalf("unexpected started push: %+v", got[0])
	}
	if !strings.Contains(got[0].Msg.Body, "Single Box Point") {
		t.Errorf("body should name the washing point, got %q", got[0].Msg.Body)
	}

	env.setStatus(t, fx, id, "ready")
	got = env.push.waitForPush(t, 2)
	if got[1].Msg.Title != "Машина готова" || got[1].Msg.Data["kind"] != "finished" {
		t.Fatalf("unexpected finished push: %+v", got[1])
	}

	list := env.do(t, http.MethodGet, "/api/v1/me/notifications", fx.customerAccess, nil)
	recs := items(t, list)
	if len(recs) != 2 || recs[0]["status"] != "sent" || recs[0]["channel"] != "push" {
		t.Fatalf("both stages should be recorded as sent push notifications: %v", list.body)
	}
}

func TestPush_NoDeviceMeansNoNotificationButStatusStillChanges(t *testing.T) {
	env := newTestEnv(t)
	fx := env.setUpPushFixture(t, 420)
	id, _ := env.book(t, fx, 2)

	env.setStatus(t, fx, id, "waiting")
	env.setStatus(t, fx, id, "washing")
	time.Sleep(300 * time.Millisecond)
	if got := env.push.snapshot(); len(got) != 0 {
		t.Fatalf("no registered device, nothing to send, got %+v", got)
	}
	if recs := items(t, env.do(t, http.MethodGet, "/api/v1/me/notifications", fx.customerAccess, nil)); len(recs) != 0 {
		t.Fatalf("nothing should be recorded either, got %v", recs)
	}
}

func TestPush_DeadTokensArePrunedAndLiveOnesStillReceive(t *testing.T) {
	env := newTestEnv(t)
	fx := env.setUpPushFixture(t, 430)
	env.registerDevice(t, fx.customerAccess, "tok-dead")
	env.registerDevice(t, fx.customerAccess, "tok-live")
	env.push.markUnregistered("tok-dead")
	id, _ := env.book(t, fx, 2)

	env.setStatus(t, fx, id, "waiting")
	env.setStatus(t, fx, id, "washing")
	got := env.push.waitForPush(t, 1)
	if len(got) != 1 || got[0].Token != "tok-live" {
		t.Fatalf("only the live device should receive it, got %+v", got)
	}

	deadline := time.Now().Add(3 * time.Second)
	var dead int64
	for time.Now().Before(deadline) {
		env.db.Raw("SELECT count(*) FROM device_tokens WHERE token = 'tok-dead'").Scan(&dead)
		if dead == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the unregistered token should have been deleted")
}

var businessTZ = time.FixedZone("Asia/Dushanbe", 5*60*60)

// newCustomerWithDevice signs up another customer who has the app installed.
func (e *testEnv) newCustomerWithDevice(t *testing.T, seed int64, token string) string {
	t.Helper()
	access := e.loginAs(t, uniquePhone(seed), "")
	e.registerDevice(t, access, token)
	return access
}

type schedulerHarness struct {
	env       *testEnv
	scheduler interface {
		Tick(ctx context.Context, now time.Time) error
	}
}

func newSchedulerHarness(t *testing.T, env *testEnv) schedulerHarness {
	return schedulerHarness{env: env, scheduler: app.NewNotificationScheduler(env.db, env.sms, env.push, time.Minute)}
}

func (h schedulerHarness) tick(t *testing.T, now time.Time) {
	t.Helper()
	if err := h.scheduler.Tick(context.Background(), now); err != nil {
		t.Fatalf("tick: %v", err)
	}
}

func (e *testEnv) kindsFor(token string) []string {
	var out []string
	for _, p := range e.push.snapshot() {
		if p.Token == token {
			out = append(out, p.Msg.Data["kind"])
		}
	}
	return out
}

func equalKinds(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestPush_SchedulerReminderAndLateNudge(t *testing.T) {
	env := newTestEnv(t)
	fx := env.setUpPushFixture(t, 440)
	env.registerDevice(t, fx.customerAccess, "tok-1")
	h := newSchedulerHarness(t, env)
	_, start := env.book(t, fx, 2)

	h.tick(t, start.Add(-61*time.Minute))
	if got := env.kindsFor("tok-1"); len(got) != 0 {
		t.Fatalf("more than an hour ahead: nothing yet, got %v", got)
	}

	h.tick(t, start.Add(-59*time.Minute))
	h.tick(t, start.Add(-30*time.Minute))
	if got := env.kindsFor("tok-1"); !equalKinds(got, "reminder_1h") {
		t.Fatalf("expected exactly one reminder, got %v", got)
	}
	msg := env.push.snapshot()[0].Msg
	if msg.Title != "Скоро мойка" || !strings.Contains(msg.Body, start.In(businessTZ).Format("15:04")) {
		t.Errorf("the reminder should carry the local start time, got %+v", msg)
	}

	h.tick(t, start.Add(4*time.Minute))
	if got := env.kindsFor("tok-1"); len(got) != 1 {
		t.Fatalf("not yet 5 minutes late, got %v", got)
	}
	h.tick(t, start.Add(6*time.Minute))
	h.tick(t, start.Add(20*time.Minute))
	if got := env.kindsFor("tok-1"); !equalKinds(got, "reminder_1h", "late_5m") {
		t.Fatalf("expected the reminder then a single late nudge, got %v", got)
	}
}

func TestPush_SchedulerSkipsWhatNoLongerApplies(t *testing.T) {
	env := newTestEnv(t)
	fx := env.setUpPushFixture(t, 450)
	h := newSchedulerHarness(t, env)

	// Started washing before its late time.
	washing := env.newCustomerWithDevice(t, 460, "tok-washing")
	washingID, washingStart := env.bookAs(t, fx, washing, 2)
	env.setStatus(t, fx, washingID, "waiting")
	env.setStatus(t, fx, washingID, "washing")
	env.push.waitForPush(t, 1)

	// Canceled before the reminder.
	canceled := env.newCustomerWithDevice(t, 461, "tok-canceled")
	canceledID, canceledStart := env.bookAs(t, fx, canceled, 3)
	if r := env.do(t, http.MethodPatch, "/api/v1/queue/"+canceledID+"/cancel", canceled, nil); r.status != http.StatusOK {
		t.Fatalf("cancel: %d", r.status)
	}

	// Booked less than an hour before its start: no reminder, but still a late nudge.
	lastMinute := env.newCustomerWithDevice(t, 462, "tok-lastminute")
	lastMinuteID, lastMinuteStart := env.bookAs(t, fx, lastMinute, 4)
	env.db.Exec("UPDATE queue SET created_at = ? WHERE id = ?", lastMinuteStart.Add(-10*time.Minute), lastMinuteID)

	// Long over (past the 2h grace): never nudged, e.g. after downtime.
	stale := env.newCustomerWithDevice(t, 463, "tok-stale")
	_, staleStart := env.bookAs(t, fx, stale, 5)

	// No app installed: nothing to send to.
	env.bookAs(t, fx, env.loginAs(t, uniquePhone(464), ""), 6)

	for _, s := range []time.Time{washingStart, canceledStart, lastMinuteStart} {
		h.tick(t, s.Add(-30*time.Minute))
		h.tick(t, s.Add(10*time.Minute))
	}
	// Only ever seen 3h after its start (the scheduler was down): too late to bother.
	h.tick(t, staleStart.Add(3*time.Hour))

	if got := env.kindsFor("tok-washing"); !equalKinds(got, "started") {
		t.Errorf("a booking already washing gets no late nudge, got %v", got)
	}
	if got := env.kindsFor("tok-canceled"); len(got) != 0 {
		t.Errorf("a canceled booking gets nothing, got %v", got)
	}
	if got := env.kindsFor("tok-lastminute"); !equalKinds(got, "late_5m") {
		t.Errorf("a last-minute booking skips the reminder but still gets the late nudge, got %v", got)
	}
	if got := env.kindsFor("tok-stale"); len(got) != 0 {
		t.Errorf("a booking past the 2h grace gets no late nudge, got %v", got)
	}
}
