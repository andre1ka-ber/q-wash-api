package queue

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/auth"
	"q-wash-api/internal/car"
	"q-wash-api/internal/httputil"
	"q-wash-api/internal/service"
)

type dayItemResponse struct {
	ID               string     `json:"id"`
	Status           string     `json:"status"`
	Ticket           string     `json:"ticket"`
	BoxNumber        int        `json:"box_number"`
	ScheduledStartAt time.Time  `json:"scheduled_start_at"`
	ScheduledEndAt   time.Time  `json:"scheduled_end_at"`
	PausedAt         *time.Time `json:"paused_at,omitempty"`
	Source           string     `json:"source"`
	CarName          string     `json:"car_name"`
	Plate            string     `json:"plate,omitempty"`
	ClientName       string     `json:"client_name,omitempty"`
	ClientPhone      string     `json:"client_phone"`
	ServiceName      string     `json:"service_name"`
	PriceOptionName  string     `json:"price_option_name"`
	PriceCents       int        `json:"price_cents"`
}

// ticketNumberBase is where each box's per-day ticket counter starts, so
// tickets read like "A-11" rather than "A-1" (matches the cabinet design).
const ticketNumberBase = 10

// listDay is the cabinet's "Очередь" day view: every booking scheduled
// that calendar day (businessLocation), any status, enriched with the
// staff-only detail the public board deliberately hides (full phone,
// plate, client name, price). Tickets are "<box letter>-<n>", n counting
// the box's bookings that day in creation order — stable across status
// changes and across later inserts (never renumbers existing rows).
func (h *Handler) listDay(w http.ResponseWriter, r *http.Request) {
	washingPointID, ok := ownWashingPointID(w, r)
	if !ok {
		return
	}

	day, err := dayFromQuery(r)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	dayStart, dayEnd := dayBounds(day)

	rows, err := h.repo.FindByWashingPointAndRange(r.Context(), washingPointID, dayStart, dayEnd)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	items, err := h.toDayItems(r.Context(), rows)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func ticketPrefix(box int) string {
	return string(rune('A'+(box-1)%26)) + "-"
}

func (h *Handler) toDayItems(ctx context.Context, rows []Queue) ([]dayItemResponse, error) {
	refs, err := h.loadRefs(ctx, rows, true)
	if err != nil {
		return nil, err
	}
	options, err := h.serviceRepo.FindPriceOptionsByIDs(ctx, uniqueIDs(rows, func(q Queue) uuid.UUID { return q.PriceOptionID }))
	if err != nil {
		return nil, err
	}
	optionByID := make(map[uuid.UUID]service.ServicePriceOption, len(options))
	for _, o := range options {
		optionByID[o.ID] = o
	}

	byCreation := make([]Queue, len(rows))
	copy(byCreation, rows)
	sort.SliceStable(byCreation, func(i, j int) bool {
		if !byCreation[i].CreatedAt.Equal(byCreation[j].CreatedAt) {
			return byCreation[i].CreatedAt.Before(byCreation[j].CreatedAt)
		}
		return byCreation[i].ID.String() < byCreation[j].ID.String()
	})
	counters := map[int]int{}
	ticketByID := make(map[uuid.UUID]string, len(rows))
	for _, row := range byCreation {
		counters[row.BoxNumber]++
		ticketByID[row.ID] = ticketPrefix(row.BoxNumber) + strconv.Itoa(ticketNumberBase+counters[row.BoxNumber])
	}

	items := make([]dayItemResponse, len(rows))
	for i, row := range rows {
		u := refs.users[row.UserID]
		var c car.Car
		if row.CarID != nil {
			c = refs.cars[*row.CarID]
		}
		item := dayItemResponse{
			ID:               row.ID.String(),
			Status:           string(row.Status),
			Ticket:           ticketByID[row.ID],
			BoxNumber:        row.BoxNumber,
			ScheduledStartAt: row.ScheduledStartAt,
			ScheduledEndAt:   row.ScheduledEndAt,
			PausedAt:         row.PausedAt,
			Source:           string(row.Source),
			CarName:          c.Name,
			ClientPhone:      u.PhoneNumber,
			ServiceName:      refs.serviceName(row.ServiceID),
			PriceOptionName:  optionByID[row.PriceOptionID].Name,
			PriceCents:       optionByID[row.PriceOptionID].PriceCents,
		}
		if u.Name != nil {
			item.ClientName = *u.Name
		}
		if c.Plate != nil {
			item.Plate = *c.Plate
		}
		items[i] = item
	}
	return items, nil
}

type createManualBookingRequest struct {
	ServiceID        string `json:"service_id"`
	PriceOptionID    string `json:"price_option_id"`
	BoxNumber        int    `json:"box_number"`
	ScheduledStartAt string `json:"scheduled_start_at"`
	CarName          string `json:"car_name"`
	Plate            string `json:"plate"`
	ClientPhone      string `json:"client_phone"`
	ClientName       string `json:"client_name"`
}

// createManual is the cabinet's "Добавить вручную" (staff/worker/admin at
// this point): books a walk-in client, found-or-created by phone (see
// Manager.CreateManualBooking).
func (h *Handler) createManual(w http.ResponseWriter, r *http.Request) {
	washingPointID, ok := ownWashingPointID(w, r)
	if !ok {
		return
	}

	var req createManualBookingRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	serviceID, err := parseUUIDField(req.ServiceID, "service_id")
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	priceOptionID, err := parseUUIDField(req.PriceOptionID, "price_option_id")
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	start, err := parseScheduledStart(req.ScheduledStartAt)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	phone, err := auth.NormalizePhoneNumber(req.ClientPhone)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	carName := strings.TrimSpace(req.CarName)
	if len(carName) > 255 {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_car_name", "car_name is too long (max 255 chars)"))
		return
	}
	plate := strings.TrimSpace(req.Plate)
	if len(plate) > 32 {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_plate", "plate is too long (max 32 chars)"))
		return
	}
	clientName := strings.TrimSpace(req.ClientName)
	if len(clientName) > 255 {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_client_name", "client_name is too long (max 255 chars)"))
		return
	}

	q, err := h.manager.CreateManualBooking(r.Context(), CreateManualBookingInput{
		WashingPointID:   washingPointID,
		ServiceID:        serviceID,
		PriceOptionID:    priceOptionID,
		BoxNumber:        req.BoxNumber,
		ScheduledStartAt: start,
		CarName:          carName,
		Plate:            plate,
		Phone:            phone,
		ClientName:       clientName,
	})
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	items, err := h.toDayItems(r.Context(), []Queue{*q})
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, items[0])
}
