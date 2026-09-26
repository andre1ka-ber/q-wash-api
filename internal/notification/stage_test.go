package notification

import (
	"strings"
	"testing"
	"time"

	"q-wash-api/internal/queue"
)

func TestKindForStatus(t *testing.T) {
	cases := map[queue.Status]Kind{
		queue.StatusWashing:  KindStarted,
		queue.StatusReady:    KindFinished,
		queue.StatusQueue:    "",
		queue.StatusWaiting:  "",
		queue.StatusCanceled: "",
		queue.StatusNoShow:   "",
	}
	for status, want := range cases {
		if got := kindForStatus(status); got != want {
			t.Errorf("%s: got %q, want %q", status, got, want)
		}
	}
}

func TestStageMessageText(t *testing.T) {
	// 05:30 UTC is 10:30 in Asia/Dushanbe.
	start := time.Date(2026, 9, 28, 5, 30, 0, 0, time.UTC)

	if m := stageMessage(KindReminder, "Pegasus", 2, start); !strings.Contains(m.Body, "10:30") || !strings.Contains(m.Body, "Pegasus") {
		t.Errorf("reminder must show the local start time and the point: %+v", m)
	}
	if m := stageMessage(KindLate, "Pegasus", 2, start); !strings.Contains(m.Body, "боксу 2") {
		t.Errorf("late nudge must name the box: %+v", m)
	}
	if m := stageMessage(KindStarted, "Pegasus", 2, start); m.Title != "Мойка началась" {
		t.Errorf("unexpected started title: %+v", m)
	}
	if m := stageMessage(KindFinished, "Pegasus", 2, start); m.Title != "Машина готова" {
		t.Errorf("unexpected finished title: %+v", m)
	}
}

func TestDueWindowsAreOrderedAndReminderNeedsLeadTime(t *testing.T) {
	r, l := dueWindows[KindReminder], dueWindows[KindLate]
	if r.from >= r.until || l.from >= l.until {
		t.Fatalf("each window must be non-empty: %+v %+v", r, l)
	}
	if r.until > l.from {
		t.Fatal("the reminder window must end before the late window starts")
	}
	if !r.needsLeadTime || l.needsLeadTime {
		t.Fatal("only the reminder skips bookings made inside its own lead time")
	}
}
