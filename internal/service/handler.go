package service

import (
	"net/http"
	"strings"

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

// RegisterRoutes mounts the service and price-option endpoints. Reads are
// public; writes require requireManage (typically RequireAuth +
// RequireRole(staff, admin)), applied per-method via chi's With().
func (h *Handler) RegisterRoutes(r chi.Router, requireManage ...func(http.Handler) http.Handler) {
	r.Route("/washing-points/{washingPointID}/services", func(wp chi.Router) {
		wp.Get("/", h.listByWashingPoint)
		wp.With(requireManage...).Post("/", h.create)
	})

	r.Route("/services/{id}", func(svc chi.Router) {
		svc.Get("/", h.get)
		svc.With(requireManage...).Patch("/", h.update)
		svc.With(requireManage...).Delete("/", h.deactivate)
		svc.With(requireManage...).Post("/price-options", h.addPriceOption)
	})

	r.Route("/price-options/{id}", func(po chi.Router) {
		po.With(requireManage...).Patch("/", h.updatePriceOption)
		po.With(requireManage...).Delete("/", h.deletePriceOption)
	})
}

type priceOptionResponse struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	PriceCents int    `json:"price_cents"`
	IsDefault  bool   `json:"is_default"`
}

func toPriceOptionResponse(opt *ServicePriceOption) priceOptionResponse {
	return priceOptionResponse{
		ID:         opt.ID.String(),
		Name:       opt.Name,
		PriceCents: opt.PriceCents,
		IsDefault:  opt.IsDefault,
	}
}

type response struct {
	ID              string                `json:"id"`
	WashingPointID  string                `json:"washing_point_id"`
	Name            string                `json:"name"`
	Description     *string               `json:"description,omitempty"`
	DurationMinutes int                   `json:"duration_minutes"`
	PictureURL      *string               `json:"picture_url,omitempty"`
	IsActive        bool                  `json:"is_active"`
	PriceOptions    []priceOptionResponse `json:"price_options"`
}

func toResponse(svc *Service) response {
	options := make([]priceOptionResponse, len(svc.PriceOptions))
	for i := range svc.PriceOptions {
		options[i] = toPriceOptionResponse(&svc.PriceOptions[i])
	}
	return response{
		ID:              svc.ID.String(),
		WashingPointID:  svc.WashingPointID.String(),
		Name:            svc.Name,
		Description:     svc.Description,
		DurationMinutes: svc.DurationMinutes,
		PictureURL:      svc.PictureURL,
		IsActive:        svc.IsActive,
		PriceOptions:    options,
	}
}

func (h *Handler) listByWashingPoint(w http.ResponseWriter, r *http.Request) {
	washingPointID, err := httputil.ParseUUIDParam(r, "washingPointID")
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	services, err := h.repo.ListByWashingPoint(r.Context(), washingPointID)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	items := make([]response, len(services))
	for i := range services {
		items[i] = toResponse(&services[i])
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := httputil.ParseUUIDParam(r, "id")
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	svc, err := h.repo.FindByID(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, toResponse(svc))
}

type priceOptionCreateInput struct {
	Name       string `json:"name"`
	PriceCents int    `json:"price_cents"`
	IsDefault  bool   `json:"is_default"`
}

type createRequest struct {
	Name            string                   `json:"name"`
	Description     *string                  `json:"description"`
	DurationMinutes int                      `json:"duration_minutes"`
	PictureURL      *string                  `json:"picture_url"`
	PriceOptions    []priceOptionCreateInput `json:"price_options"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	authUser, ok := httputil.AuthUser(w, r)
	if !ok {
		return
	}

	washingPointID, err := httputil.ParseUUIDParam(r, "washingPointID")
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if _, err := h.wpRepo.FindByID(r.Context(), washingPointID); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if !authUser.OwnsWashingPoint(washingPointID) {
		httputil.WriteError(w, r, apperror.NotFound("washing_point_not_found", "washing point not found"))
		return
	}

	var req createRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 255 {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_name", "name is required (max 255 chars)"))
		return
	}
	if req.DurationMinutes <= 0 {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_duration", "duration_minutes must be positive"))
		return
	}
	if req.PictureURL != nil && len(*req.PictureURL) > 500 {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_picture_url", "picture_url is too long (max 500 chars)"))
		return
	}
	if len(req.PriceOptions) == 0 {
		httputil.WriteError(w, r, apperror.BadRequest("price_options_required", "at least one price option is required"))
		return
	}

	inputs := make([]PriceOptionInput, len(req.PriceOptions))
	defaultCount := 0
	for i, po := range req.PriceOptions {
		poName := strings.TrimSpace(po.Name)
		if poName == "" || len(poName) > 255 {
			httputil.WriteError(w, r, apperror.BadRequest("invalid_price_option_name", "price option name is required (max 255 chars)"))
			return
		}
		if po.PriceCents < 0 {
			httputil.WriteError(w, r, apperror.BadRequest("invalid_price_cents", "price_cents must not be negative"))
			return
		}
		if po.IsDefault {
			defaultCount++
		}
		inputs[i] = PriceOptionInput{Name: poName, PriceCents: po.PriceCents, IsDefault: po.IsDefault}
	}
	if defaultCount > 1 {
		httputil.WriteError(w, r, apperror.BadRequest("multiple_default_price_options", "only one price option can be marked default"))
		return
	}
	if defaultCount == 0 {
		inputs[0].IsDefault = true
	}

	var description *string
	if req.Description != nil {
		d := strings.TrimSpace(*req.Description)
		if len(d) > 2000 {
			httputil.WriteError(w, r, apperror.BadRequest("invalid_description", "description is too long (max 2000 chars)"))
			return
		}
		description = &d
	}

	svc, err := h.manager.CreateService(r.Context(), washingPointID, name, description, req.DurationMinutes, req.PictureURL, inputs)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, toResponse(svc))
}

type updateRequest struct {
	Name            *string `json:"name"`
	Description     *string `json:"description"`
	DurationMinutes *int    `json:"duration_minutes"`
	PictureURL      *string `json:"picture_url"`
	IsActive        *bool   `json:"is_active"`
}

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
	svc, err := h.repo.FindByID(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if !authUser.OwnsWashingPoint(svc.WashingPointID) {
		httputil.WriteError(w, r, apperror.NotFound("service_not_found", "service not found"))
		return
	}

	var req updateRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" || len(name) > 255 {
			httputil.WriteError(w, r, apperror.BadRequest("invalid_name", "name is required (max 255 chars)"))
			return
		}
		svc.Name = name
	}
	if req.Description != nil {
		d := strings.TrimSpace(*req.Description)
		if len(d) > 2000 {
			httputil.WriteError(w, r, apperror.BadRequest("invalid_description", "description is too long (max 2000 chars)"))
			return
		}
		svc.Description = &d
	}
	if req.DurationMinutes != nil {
		if *req.DurationMinutes <= 0 {
			httputil.WriteError(w, r, apperror.BadRequest("invalid_duration", "duration_minutes must be positive"))
			return
		}
		svc.DurationMinutes = *req.DurationMinutes
	}
	if req.PictureURL != nil {
		if len(*req.PictureURL) > 500 {
			httputil.WriteError(w, r, apperror.BadRequest("invalid_picture_url", "picture_url is too long (max 500 chars)"))
			return
		}
		svc.PictureURL = req.PictureURL
	}
	if req.IsActive != nil {
		svc.IsActive = *req.IsActive
	}

	if err := h.repo.Update(r.Context(), svc); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, toResponse(svc))
}

func (h *Handler) deactivate(w http.ResponseWriter, r *http.Request) {
	authUser, ok := httputil.AuthUser(w, r)
	if !ok {
		return
	}

	id, err := httputil.ParseUUIDParam(r, "id")
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	svc, err := h.repo.FindByID(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if !authUser.OwnsWashingPoint(svc.WashingPointID) {
		httputil.WriteError(w, r, apperror.NotFound("service_not_found", "service not found"))
		return
	}
	if err := h.repo.Deactivate(r.Context(), id); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) addPriceOption(w http.ResponseWriter, r *http.Request) {
	authUser, ok := httputil.AuthUser(w, r)
	if !ok {
		return
	}

	serviceID, err := httputil.ParseUUIDParam(r, "id")
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	svc, err := h.repo.FindByID(r.Context(), serviceID)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if !authUser.OwnsWashingPoint(svc.WashingPointID) {
		httputil.WriteError(w, r, apperror.NotFound("service_not_found", "service not found"))
		return
	}

	var req priceOptionCreateInput
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 255 {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_price_option_name", "price option name is required (max 255 chars)"))
		return
	}
	if req.PriceCents < 0 {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_price_cents", "price_cents must not be negative"))
		return
	}

	opt, err := h.manager.AddPriceOption(r.Context(), serviceID, name, req.PriceCents, req.IsDefault)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, toPriceOptionResponse(opt))
}

type updatePriceOptionRequest struct {
	Name       *string `json:"name"`
	PriceCents *int    `json:"price_cents"`
	IsDefault  *bool   `json:"is_default"`
}

func (h *Handler) updatePriceOption(w http.ResponseWriter, r *http.Request) {
	authUser, ok := httputil.AuthUser(w, r)
	if !ok {
		return
	}

	id, err := httputil.ParseUUIDParam(r, "id")
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if err := h.checkOwnsPriceOption(r, authUser, id); err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	var req updatePriceOptionRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	var name *string
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		if trimmed == "" || len(trimmed) > 255 {
			httputil.WriteError(w, r, apperror.BadRequest("invalid_price_option_name", "price option name is required (max 255 chars)"))
			return
		}
		name = &trimmed
	}
	if req.PriceCents != nil && *req.PriceCents < 0 {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_price_cents", "price_cents must not be negative"))
		return
	}

	opt, err := h.manager.UpdatePriceOption(r.Context(), id, name, req.PriceCents, req.IsDefault)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, toPriceOptionResponse(opt))
}

func (h *Handler) deletePriceOption(w http.ResponseWriter, r *http.Request) {
	authUser, ok := httputil.AuthUser(w, r)
	if !ok {
		return
	}

	id, err := httputil.ParseUUIDParam(r, "id")
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if err := h.checkOwnsPriceOption(r, authUser, id); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if err := h.manager.DeletePriceOption(r.Context(), id); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// checkOwnsPriceOption resolves a price option to its parent service's
// washing point and verifies authUser may act on it — priceOptionID alone
// (unlike serviceID) doesn't reveal which point it belongs to, so both
// updatePriceOption and deletePriceOption need this extra hop.
func (h *Handler) checkOwnsPriceOption(r *http.Request, authUser reqctx.AuthUser, priceOptionID uuid.UUID) error {
	opt, err := h.repo.FindPriceOptionByID(r.Context(), priceOptionID)
	if err != nil {
		return err
	}
	svc, err := h.repo.FindByID(r.Context(), opt.ServiceID)
	if err != nil {
		return err
	}
	if !authUser.OwnsWashingPoint(svc.WashingPointID) {
		return apperror.NotFound("price_option_not_found", "price option not found")
	}
	return nil
}
