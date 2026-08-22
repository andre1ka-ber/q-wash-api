package queue

import (
	"testing"
	"time"
)

func mkQueue(boxNumber int, start, end time.Time) Queue {
	return Queue{BoxNumber: boxNumber, ScheduledStartAt: start, ScheduledEndAt: end}
}

func TestVerifyBoxAvailable_NoBusyBookings(t *testing.T) {
	if err := verifyBoxAvailable(1, day(9, 0), day(9, 30), nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVerifyBoxAvailable_RequestedBoxTaken(t *testing.T) {
	busy := []Queue{mkQueue(1, day(9, 0), day(9, 30))}
	if err := verifyBoxAvailable(1, day(9, 0), day(9, 30), busy); err == nil {
		t.Fatal("expected an error requesting a box that's already occupied for that window")
	}
}

func TestVerifyBoxAvailable_OtherBoxTakenDoesNotBlock(t *testing.T) {
	// Box 2 is free even though box 1 is busy for the same window.
	busy := []Queue{mkQueue(1, day(9, 0), day(9, 30))}
	if err := verifyBoxAvailable(2, day(9, 0), day(9, 30), busy); err != nil {
		t.Errorf("expected box 2 to be available (box 1 busy is irrelevant), got %v", err)
	}
}

func TestVerifyBoxAvailable_NonOverlappingBookingDoesNotBlock(t *testing.T) {
	// Box 1 is busy earlier in the day and doesn't overlap the candidate
	// window, so it should still be available.
	busy := []Queue{mkQueue(1, day(7, 0), day(7, 30))}
	if err := verifyBoxAvailable(1, day(9, 0), day(9, 30), busy); err != nil {
		t.Errorf("expected box 1 to be free (no overlap), got %v", err)
	}
}

func TestVerifyBoxAvailable_BackToBackNotOverlapping(t *testing.T) {
	// A booking ending exactly when the candidate starts must not count as
	// occupying the box — matches the DB EXCLUDE constraint's half-open
	// range semantics (see availability_test.go for the same rule applied
	// to the availability sweep).
	busy := []Queue{mkQueue(1, day(8, 30), day(9, 0))}
	if err := verifyBoxAvailable(1, day(9, 0), day(9, 30), busy); err != nil {
		t.Errorf("expected box 1 to be available right after the prior booking ends, got %v", err)
	}
}

func TestForwardStatusTransitions_OnlyForwardOneStep(t *testing.T) {
	want := map[Status]Status{
		StatusQueue:   StatusWaiting,
		StatusWaiting: StatusWashing,
		StatusWashing: StatusReady,
	}
	if len(forwardStatusTransitions) != len(want) {
		t.Fatalf("expected %d entries, got %d", len(want), len(forwardStatusTransitions))
	}
	for from, to := range want {
		if got := forwardStatusTransitions[from]; got != to {
			t.Errorf("expected %q -> %q, got %q -> %q", from, to, from, got)
		}
	}
}

func TestForwardStatusTransitions_TerminalStatesHaveNoEntry(t *testing.T) {
	for _, terminal := range []Status{StatusReady, StatusCanceled} {
		if _, ok := forwardStatusTransitions[terminal]; ok {
			t.Errorf("expected %q to be terminal (no forward transition), but found one", terminal)
		}
	}
}
