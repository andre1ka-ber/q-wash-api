package qrcode

import (
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/httputil"
	"q-wash-api/internal/platform/reqctx"
	"q-wash-api/internal/washingpoint"
)

type Handler struct {
	repo    *Repository
	manager *Manager
	wpRepo  *washingpoint.Repository
}

func NewHandler(repo *Repository, manager *Manager, wpRepo *washingpoint.Repository) *Handler {
	return &Handler{repo: repo, manager: manager, wpRepo: wpRepo}
}

// RegisterRoutes mounts every /qr-codes* surface in one place (rather than
// three separate registration methods) so the static /qr-codes/mine and
// /qr-codes/scan/{token} paths are guaranteed to sit alongside the dynamic
// /qr-codes/{id} routes under a single chi.Route call — avoids any
// ambiguity about route-merge order across multiple Route calls for the
// same prefix.
func (h *Handler) RegisterRoutes(r chi.Router, requireAdmin []func(http.Handler) http.Handler, requireStaff []func(http.Handler) http.Handler) {
	r.Route("/qr-codes", func(qr chi.Router) {
		qr.With(requireStaff...).Get("/mine", h.getMine)
		qr.With(requireStaff...).Post("/mine/request-replacement", h.requestReplacement)
		qr.Get("/scan/{token}", h.scan)

		qr.With(requireAdmin...).Post("/generate", h.generate)
		qr.With(requireAdmin...).Get("/", h.list)
		qr.With(requireAdmin...).Get("/{id}", h.get)
		qr.With(requireAdmin...).Post("/{id}/assign", h.assign)
		qr.With(requireAdmin...).Post("/{id}/unassign", h.unassign)
		qr.With(requireAdmin...).Post("/{id}/disable", h.disable)
	})
}

type statsResponse struct {
	ScansToday    int64      `json:"scans_today"`
	Scans7d       int64      `json:"scans_7d"`
	ScansByDay    []DayCount `json:"scans_by_day"`
	BookingsViaQR int64      `json:"bookings_via_qr"`
}

type response struct {
	ID                     string         `json:"id"`
	Code                   string         `json:"code"`
	Token                  string         `json:"token"`
	Status                 string         `json:"status"`
	BatchLabel             string         `json:"batch_label"`
	WashingPointID         *string        `json:"washing_point_id"`
	WashingPointName       *string        `json:"washing_point_name"`
	AssignedAt             *time.Time     `json:"assigned_at"`
	DisabledAt             *time.Time     `json:"disabled_at"`
	ReplacementRequestedAt *time.Time     `json:"replacement_requested_at"`
	CreatedAt              time.Time      `json:"created_at"`
	Stats                  *statsResponse `json:"stats,omitempty"`
}

func (h *Handler) toResponse(c *QRCode, wp *washingpoint.WashingPoint, stats *Stats) response {
	resp := response{
		ID:                     c.ID.String(),
		Code:                   c.Code(),
		Token:                  c.Token,
		Status:                 string(c.Status),
		BatchLabel:             c.BatchLabel,
		AssignedAt:             c.AssignedAt,
		DisabledAt:             c.DisabledAt,
		ReplacementRequestedAt: c.ReplacementRequestedAt,
		CreatedAt:              c.CreatedAt,
	}
	if c.WashingPointID != nil {
		id := c.WashingPointID.String()
		resp.WashingPointID = &id
		if wp != nil {
			resp.WashingPointName = &wp.Name
		}
	}
	if stats != nil {
		days := make([]DayCount, len(stats.ScansByDay))
		copy(days, stats.ScansByDay[:])
		resp.Stats = &statsResponse{
			ScansToday:    stats.ScansToday,
			Scans7d:       stats.Scans7d,
			ScansByDay:    days,
			BookingsViaQR: stats.BookingsViaQR,
		}
	}
	return resp
}

// detail resolves a code's washing point name (when assigned) and stats,
// then writes the full response shape — shared by every handler that
// returns one code after a mutation or a direct lookup.
func (h *Handler) detail(w http.ResponseWriter, r *http.Request, c *QRCode, status int) {
	var wp *washingpoint.WashingPoint
	if c.WashingPointID != nil {
		found, err := h.wpRepo.FindByID(r.Context(), *c.WashingPointID)
		if err != nil {
			httputil.WriteError(w, r, err)
			return
		}
		wp = found
	}
	stats, err := h.manager.Stats(r.Context(), c)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, status, h.toResponse(c, wp, &stats))
}

type generateRequest struct {
	Count      int    `json:"count"`
	BatchLabel string `json:"batch_label"`
}

func (h *Handler) generate(w http.ResponseWriter, r *http.Request) {
	var req generateRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	batchLabel := strings.TrimSpace(req.BatchLabel)
	if batchLabel == "" || len(batchLabel) > 255 {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_batch_label", "batch_label is required (max 255 chars)"))
		return
	}

	codes, err := h.manager.GenerateBatch(r.Context(), req.Count, batchLabel)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	items := make([]response, len(codes))
	for i := range codes {
		items[i] = h.toResponse(&codes[i], nil, nil)
	}
	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"items": items})
}

type poolStats struct {
	Total    int `json:"total"`
	Free     int `json:"free"`
	Assigned int `json:"assigned"`
	Disabled int `json:"disabled"`
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	all, err := h.repo.List(r.Context(), nil)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	stats := poolStats{Total: len(all)}
	for _, c := range all {
		switch c.Status {
		case StatusFree:
			stats.Free++
		case StatusAssigned:
			stats.Assigned++
		case StatusDisabled:
			stats.Disabled++
		}
	}

	var statusFilter *QRCodeStatus
	if raw := r.URL.Query().Get("status"); raw != "" {
		st := QRCodeStatus(raw)
		if st != StatusFree && st != StatusAssigned && st != StatusDisabled {
			httputil.WriteError(w, r, apperror.BadRequest("invalid_status", "status must be one of: free, assigned, disabled"))
			return
		}
		statusFilter = &st
	}
	search := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("search")))

	// Resolve washing point names once for the assigned subset, rather
	// than a per-row query.
	wpNames := make(map[uuid.UUID]string)

	items := make([]response, 0, len(all))
	for i := range all {
		c := &all[i]
		if statusFilter != nil && c.Status != *statusFilter {
			continue
		}
		if search != "" && !strings.Contains(strings.ToUpper(c.Code()), search) && !strings.Contains(strings.ToUpper(c.Token), search) {
			continue
		}
		var wp *washingpoint.WashingPoint
		if c.WashingPointID != nil {
			name, ok := wpNames[*c.WashingPointID]
			if !ok {
				found, err := h.wpRepo.FindByID(r.Context(), *c.WashingPointID)
				if err != nil {
					httputil.WriteError(w, r, err)
					return
				}
				name = found.Name
				wpNames[*c.WashingPointID] = name
			}
			wp = &washingpoint.WashingPoint{Name: name}
		}
		items = append(items, h.toResponse(c, wp, nil))
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "stats": stats})
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
	h.detail(w, r, c, http.StatusOK)
}

type assignRequest struct {
	WashingPointID string `json:"washing_point_id"`
}

func (h *Handler) assign(w http.ResponseWriter, r *http.Request) {
	id, err := httputil.ParseUUIDParam(r, "id")
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	var req assignRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	wpID, err := uuid.Parse(req.WashingPointID)
	if err != nil {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_washing_point_id", "invalid washing_point_id"))
		return
	}
	c, err := h.manager.Assign(r.Context(), id, wpID)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	h.detail(w, r, c, http.StatusOK)
}

func (h *Handler) unassign(w http.ResponseWriter, r *http.Request) {
	id, err := httputil.ParseUUIDParam(r, "id")
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	c, err := h.manager.Unassign(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	h.detail(w, r, c, http.StatusOK)
}

func (h *Handler) disable(w http.ResponseWriter, r *http.Request) {
	id, err := httputil.ParseUUIDParam(r, "id")
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	c, err := h.manager.Disable(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	h.detail(w, r, c, http.StatusOK)
}

func (h *Handler) getMine(w http.ResponseWriter, r *http.Request) {
	authUser, ok := reqctx.AuthUserFromContext(r.Context())
	if !ok {
		httputil.WriteError(w, r, apperror.Unauthorized("unauthenticated", "authentication required"))
		return
	}
	if authUser.WashingPointID == nil {
		httputil.WriteError(w, r, apperror.BadRequest("no_washing_point", "this account isn't scoped to a washing point"))
		return
	}
	c, err := h.repo.FindByWashingPointID(r.Context(), *authUser.WashingPointID)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	h.detail(w, r, c, http.StatusOK)
}

func (h *Handler) requestReplacement(w http.ResponseWriter, r *http.Request) {
	authUser, ok := reqctx.AuthUserFromContext(r.Context())
	if !ok {
		httputil.WriteError(w, r, apperror.Unauthorized("unauthenticated", "authentication required"))
		return
	}
	if authUser.WashingPointID == nil {
		httputil.WriteError(w, r, apperror.BadRequest("no_washing_point", "this account isn't scoped to a washing point"))
		return
	}
	c, err := h.repo.FindByWashingPointID(r.Context(), *authUser.WashingPointID)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	c, err = h.manager.RequestReplacement(r.Context(), c.ID)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	h.detail(w, r, c, http.StatusOK)
}

var scanPageTmpl = template.Must(template.New("scan").Parse(`<!DOCTYPE html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Q Wash</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Sora:wght@400;600;700&display=swap" rel="stylesheet">
<style>
  body { margin:0; min-height:100vh; display:flex; align-items:center; justify-content:center; background:#0A0A09; font-family:'Sora',system-ui,sans-serif; color:#F1F0E9; padding:24px; box-sizing:border-box; }
  .card { max-width:380px; width:100%; text-align:center; display:flex; flex-direction:column; gap:16px; align-items:center; }
  h1 { font-size:22px; font-weight:700; margin:0; letter-spacing:-.02em; }
  p { color:#93918A; font-size:14px; margin:0; line-height:1.5; }
  a.btn { display:inline-block; margin-top:8px; padding:14px 28px; border-radius:14px; background:#F2D14B; color:#191813; font-weight:700; text-decoration:none; font-size:15px; }
  .fine { font-size:12px; color:#6E6E66; margin-top:12px; }
</style>
</head>
<body>
<div class="card">
  <h1>{{.Heading}}</h1>
  <p>{{.Subtext}}</p>
  {{if .DeepLink}}<a class="btn" href="{{.DeepLink}}">Открыть в приложении Q Wash</a>
  <p class="fine">Если приложение ещё не установлено, скачайте Q Wash из App Store или Google Play.</p>{{end}}
</div>
</body>
</html>`))

type scanPageData struct {
	Heading  string
	Subtext  string
	DeepLink string
}

func (h *Handler) scan(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")

	c, err := h.repo.FindByToken(r.Context(), token)
	if err != nil || c.Status == StatusDisabled {
		writeScanPage(w, scanPageData{
			Heading: "Код недействителен",
			Subtext: "Эта наклейка не активна. Обратитесь к персоналу мойки.",
		})
		return
	}

	// Best-effort: a failed scan write shouldn't error the customer's
	// phone — still serve the page either way.
	_ = h.repo.CreateScan(r.Context(), c.ID)

	data := scanPageData{
		Heading: "Сканируйте, чтобы занять очередь",
		Subtext: "услуги · запись · живая очередь",
	}
	if c.WashingPointID != nil {
		if wp, err := h.wpRepo.FindByID(r.Context(), *c.WashingPointID); err == nil {
			data.Heading = wp.Name
			data.DeepLink = "qwash://point/" + wp.ID.String()
		}
	}
	writeScanPage(w, data)
}

func writeScanPage(w http.ResponseWriter, data scanPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = scanPageTmpl.Execute(w, data)
}
