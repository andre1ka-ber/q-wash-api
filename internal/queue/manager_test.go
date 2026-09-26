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

func TestStaffStatusTransitions(t *testing.T) {
	want := map[Status][]Status{
		StatusQueue:    {StatusWaiting, StatusNoShow},
		StatusWaiting:  {StatusWashing, StatusNoShow},
		StatusWashing:  {StatusReady},
		StatusNoShow:   {StatusQueue},
		StatusCanceled: {StatusQueue},
	}
	if len(staffStatusTransitions) != len(want) {
		t.Fatalf("expected %d entries, got %d", len(want), len(staffStatusTransitions))
	}
	for from, tos := range want {
		got := staffStatusTransitions[from]
		if len(got) != len(tos) {
			t.Errorf("%q: expected %v, got %v", from, tos, got)
			continue
		}
		for i := range tos {
			if got[i] != tos[i] {
				t.Errorf("%q: expected %v, got %v", from, tos, got)
			}
		}
	}
}

func TestStaffStatusTransitions_ReadyIsTerminal(t *testing.T) {
	if _, ok := staffStatusTransitions[StatusReady]; ok {
		t.Error("expected ready to be terminal (no staff transition)")
	}
}

func TestTicketPrefix(t *testing.T) {
	for box, want := range map[int]string{1: "A-", 2: "B-", 26: "Z-", 27: "A-"} {
		if got := ticketPrefix(box); got != want {
			t.Errorf("box %d: expected %q, got %q", box, want, got)
		}
	}
}
