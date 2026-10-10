package connectionrequest

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/httputil"
	"q-wash-api/internal/washingpoint"
)

type Handler struct {
	repo    *Repository
	manager *Manager
}

func NewHandler(repo *Repository, manager *Manager) *Handler {
	return &Handler{repo: repo, manager: manager}
}

// RegisterRoutes mounts /connection-requests. Everything is admin-only (the
// admin app's onboarding queue, not a staff-facing surface) except
// POST /connection-requests/apply, the public landing-page form, which is
// unauthenticated and so goes through publicLimit (a rate limiter).
func (h *Handler) RegisterRoutes(r chi.Router, publicLimit func(http.Handler) http.Handler, requireAdmin ...func(http.Handler) http.Handler) {
	r.Route("/connection-requests", func(cr chi.Router) {
		cr.With(publicLimit).Post("/apply", h.apply)
		cr.With(requireAdmin...).Get("/", h.list)
		cr.With(requireAdmin...).Get("/{id}", h.get)
		cr.With(requireAdmin...).Post("/", h.create)
		cr.With(requireAdmin...).Patch("/{id}", h.update)
	})
}

type response struct {
	ID           string     `json:"id"`
	BusinessName string     `json:"business_name"`
	ContactName  *string    `json:"contact_name,omitempty"`
	ContactPhone string     `json:"contact_phone"`
	Address      *string    `json:"address,omitempty"`
	BoxesCount   *int       `json:"boxes_count,omitempty"`
	Note         *string    `json:"note,omitempty"`
	Status       string     `json:"status"`
	ReviewedBy   *string    `json:"reviewed_by,omitempty"`
	ReviewedAt   *time.Time `json:"reviewed_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// approveResponse is only ever returned by the approve branch of
// updateStatus — the one moment the newly-provisioned washing point's
// staff/worker passwords exist in plaintext anywhere (see
// washingpoint.createResponse's doc comment; same reasoning applies here).
type approveResponse struct {
	response
	Credentials washingpoint.AccountsResponse `json:"credentials"`
}

func toResponse(c *ConnectionRequest) response {
	var reviewedBy *string
	if c.ReviewedBy != nil {
		s := c.ReviewedBy.String()
		reviewedBy = &s
	}
	return response{
		ID:           c.ID.String(),
		BusinessName: c.BusinessName,
		ContactName:  c.ContactName,
		ContactPhone: c.ContactPhone,
		Address:      c.Address,
		BoxesCount:   c.BoxesCount,
		Note:         c.Note,
		Status:       string(c.Status),
		ReviewedBy:   reviewedBy,
		ReviewedAt:   c.ReviewedAt,
		CreatedAt:    c.CreatedAt,
	}
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	var status *Status
	if raw := r.URL.Query().Get("status"); raw != "" {
		st := Status(raw)
		if st != StatusNew && st != StatusApproved && st != StatusRejected {
			httputil.WriteError(w, r, apperror.BadRequest("invalid_status", "status must be one of: new, approved, rejected"))
			return
		}
		status = &st
	}

	items, err := h.repo.List(r.Context(), status)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	resp := make([]response, len(items))
	for i := range items {
		resp[i] = toResponse(&items[i])
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": resp})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := httputil.ParseUUIDParam(r, "id")
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	c, err := h.repo.FindByID(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, toResponse(c))
}

type createRequest struct {
	BusinessName string  `json:"business_name"`
	ContactName  string  `json:"contact_name"`
	ContactPhone string  `json:"contact_phone"`
	Address      string  `json:"address"`
	BoxesCount   int     `json:"boxes_count"`
	Note         *string `json:"note"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	businessName := strings.TrimSpace(req.BusinessName)
	contactName := strings.TrimSpace(req.ContactName)
	contactPhone := strings.TrimSpace(req.ContactPhone)
	address := strings.TrimSpace(req.Address)

	if err := validate(businessName, contactName, contactPhone, address, req.BoxesCount); err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	var note *string
	if req.Note != nil {
		trimmed := strings.TrimSpace(*req.Note)
		if len(trimmed) > 2000 {
			httputil.WriteError(w, r, apperror.BadRequest("invalid_note", "note is too long (max 2000 chars)"))
			return
		}
		if trimmed != "" {
			note = &trimmed
		}
	}

	boxes := req.BoxesCount
	c := &ConnectionRequest{
		BusinessName: businessName,
		ContactName:  &contactName,
		ContactPhone: contactPhone,
		Address:      &address,
		BoxesCount:   &boxes,
		Note:         note,
		Status:       StatusNew,
	}
	if err := h.repo.Create(r.Context(), c); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, toResponse(c))
}

type applyRequest struct {
	BusinessName string  `json:"business_name"`
	ContactName  string  `json:"contact_name"`
	ContactPhone string  `json:"contact_phone"`
	Address      string  `json:"address"`
	BoxesCount   int     `json:"boxes_count"`
	Note         *string `json:"note"`
	// Website is a honeypot: the landing form renders it hidden, so a real
	// visitor never fills it. A non-empty value is treated as a bot.
	Website string `json:"website"`
}

// apply is the unauthenticated counterpart of create, backing the landing
// page's "connect your wash" form. Only a phone and a name are required —
// business_name or contact_name, at least one (business_name falls back to
// contact_name); address, boxes_count and note are optional and an admin
// completes the rest (PATCH /connection-requests/{id}) before approving.
//   - honeypot filled: answers 202 without storing anything, so a bot learns
//     nothing from the response;
//   - an unreviewed (status=new) request with the same phone already exists:
//     answers 202 without storing a duplicate (double-submits, retries);
//   - otherwise stores a status=new request.
//
// The response never echoes stored data or ids.
func (h *Handler) apply(w http.ResponseWriter, r *http.Request) {
	var req applyRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	if strings.TrimSpace(req.Website) != "" {
		httputil.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "received"})
		return
	}

	businessName := strings.TrimSpace(req.BusinessName)
	contactName := strings.TrimSpace(req.ContactName)
	contactPhone := strings.TrimSpace(req.ContactPhone)
	address := strings.TrimSpace(req.Address)

	if businessName == "" {
		businessName = contactName
	}
	if businessName == "" {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_business_name", "business_name or contact_name is required"))
		return
	}
	if len(businessName) > 255 {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_business_name", "business_name is too long (max 255 chars)"))
		return
	}
	if len(contactName) > 255 {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_contact_name", "contact_name is too long (max 255 chars)"))
		return
	}
	if !validPhone(contactPhone) || len(contactPhone) > 32 {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_contact_phone", "contact_phone must contain 9 to 15 digits"))
		return
	}
	if len(address) > 500 {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_address", "address is too long (max 500 chars)"))
		return
	}
	if req.BoxesCount < 0 || req.BoxesCount > maxApplyBoxes {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_boxes_count", "boxes_count must be between 1 and 100 when given"))
		return
	}

	var note *string
	if req.Note != nil {
		trimmed := strings.TrimSpace(*req.Note)
		if len(trimmed) > 2000 {
			httputil.WriteError(w, r, apperror.BadRequest("invalid_note", "note is too long (max 2000 chars)"))
			return
		}
		if trimmed != "" {
			note = &trimmed
		}
	}

	exists, err := h.repo.ExistsNewByPhone(r.Context(), contactPhone)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if !exists {
		c := &ConnectionRequest{
			BusinessName: businessName,
			ContactPhone: contactPhone,
			Note:         note,
			Status:       StatusNew,
		}
		if contactName != "" {
			c.ContactName = &contactName
		}
		if address != "" {
			c.Address = &address
		}
		if req.BoxesCount > 0 {
			boxes := req.BoxesCount
			c.BoxesCount = &boxes
		}
		if err := h.repo.Create(r.Context(), c); err != nil {
			httputil.WriteError(w, r, err)
			return
		}
	}
	httputil.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "received"})
}

// maxApplyBoxes caps boxes_count on the public form; the admin create path
// is trusted and has no cap.
const maxApplyBoxes = 100

// validPhone accepts any phone with 9-15 digits (E.164 maximum is 15),
// ignoring formatting characters like "+", spaces, dashes and brackets.
func validPhone(s string) bool {
	digits := 0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '+' || r == ' ' || r == '-' || r == '(' || r == ')':
		default:
			return false
		}
	}
	return digits >= 9 && digits <= 15
}

// updateRequest is the PATCH body: any subset of the editable fields, and/or
// a status transition. An empty string clears contact_name/address/note.
type updateRequest struct {
	Status       string  `json:"status"`
	BusinessName *string `json:"business_name"`
	ContactName  *string `json:"contact_name"`
	ContactPhone *string `json:"contact_phone"`
	Address      *string `json:"address"`
	BoxesCount   *int    `json:"boxes_count"`
	Note         *string `json:"note"`
}

func (u updateRequest) hasEdits() bool {
	return u.BusinessName != nil || u.ContactName != nil || u.ContactPhone != nil ||
		u.Address != nil || u.BoxesCount != nil || u.Note != nil
}

// update completes a still-new request (details the public form didn't
// collect) and/or approves or rejects it. Edits are applied and saved first,
// so `{status:"approved", address:"...", boxes_count:3}` fills the gaps and
// approves in one call. Reviewed requests can't be edited.
func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	authUser, ok := httputil.AuthUser(w, r)
	if !ok {
		return
	}

	id, err := httputil.ParseUUIDParam(r, "id")
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	var req updateRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if req.Status != "" && Status(req.Status) != StatusApproved && Status(req.Status) != StatusRejected {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_status", "status must be one of: approved, rejected"))
		return
	}
	if req.Status == "" && !req.hasEdits() {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_body", "provide a status and/or fields to update"))
		return
	}

	var current *ConnectionRequest
	if req.hasEdits() {
		current, err = h.repo.FindByID(r.Context(), id)
		if err != nil {
			httputil.WriteError(w, r, err)
			return
		}
		if current.Status != StatusNew {
			httputil.WriteError(w, r, apperror.Conflict("connection_request_already_reviewed", "connection request has already been reviewed"))
			return
		}
		if err := applyEdits(current, req); err != nil {
			httputil.WriteError(w, r, err)
			return
		}
		if err := h.repo.Update(r.Context(), current); err != nil {
			httputil.WriteError(w, r, err)
			return
		}
	}

	switch Status(req.Status) {
	case StatusApproved:
		c, _, accounts, err := h.manager.Approve(r.Context(), id, authUser.ID)
		if err != nil {
			httputil.WriteError(w, r, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, approveResponse{
			response:    toResponse(c),
			Credentials: washingpoint.ToAccountsResponse(accounts),
		})
	case StatusRejected:
		c, err := h.manager.Reject(r.Context(), id, authUser.ID)
		if err != nil {
			httputil.WriteError(w, r, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, toResponse(c))
	default:
		httputil.WriteJSON(w, http.StatusOK, toResponse(current))
	}
}

// applyEdits validates the supplied fields and writes them onto c.
func applyEdits(c *ConnectionRequest, req updateRequest) error {
	trim := func(p *string) string { return strings.TrimSpace(*p) }

	if req.BusinessName != nil {
		v := trim(req.BusinessName)
		if v == "" || len(v) > 255 {
			return apperror.BadRequest("invalid_business_name", "business_name is required (max 255 chars)")
		}
		c.BusinessName = v
	}
	if req.ContactName != nil {
		v := trim(req.ContactName)
		if len(v) > 255 {
			return apperror.BadRequest("invalid_contact_name", "contact_name is too long (max 255 chars)")
		}
		c.ContactName = nilIfEmpty(v)
	}
	if req.ContactPhone != nil {
		v := trim(req.ContactPhone)
		if v == "" || len(v) > 32 {
			return apperror.BadRequest("invalid_contact_phone", "contact_phone is required (max 32 chars)")
		}
		c.ContactPhone = v
	}
	if req.Address != nil {
		v := trim(req.Address)
		if len(v) > 500 {
			return apperror.BadRequest("invalid_address", "address is too long (max 500 chars)")
		}
		c.Address = nilIfEmpty(v)
	}
	if req.BoxesCount != nil {
		if *req.BoxesCount < 1 {
			return apperror.BadRequest("invalid_boxes_count", "boxes_count must be at least 1")
		}
		c.BoxesCount = req.BoxesCount
	}
	if req.Note != nil {
		v := trim(req.Note)
		if len(v) > 2000 {
			return apperror.BadRequest("invalid_note", "note is too long (max 2000 chars)")
		}
		c.Note = nilIfEmpty(v)
	}
	return nil
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func validate(businessName, contactName, contactPhone, address string, boxesCount int) error {
	if businessName == "" || len(businessName) > 255 {
		return apperror.BadRequest("invalid_business_name", "business_name is required (max 255 chars)")
	}
	if contactName == "" || len(contactName) > 255 {
		return apperror.BadRequest("invalid_contact_name", "contact_name is required (max 255 chars)")
	}
	if contactPhone == "" || len(contactPhone) > 32 {
		return apperror.BadRequest("invalid_contact_phone", "contact_phone is required (max 32 chars)")
	}
	if address == "" || len(address) > 500 {
		return apperror.BadRequest("invalid_address", "address is required (max 500 chars)")
	}
	if boxesCount < 1 {
		return apperror.BadRequest("invalid_boxes_count", "boxes_count must be at least 1")
	}
	return nil
}
