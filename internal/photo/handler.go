package photo

import (
	"bytes"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/httputil"
	"q-wash-api/internal/platform/reqctx"
	"q-wash-api/internal/washingpoint"
)

// maxUploadBytes caps a single photo upload; enforced via
// http.MaxBytesReader before the multipart form is parsed.
const maxUploadBytes = 10 << 20 // 10 MiB

// allowedContentTypes maps a sniffed (never client-declared) content type
// to the extension the file is stored under, so the Content-Type a static
// file server later derives from that extension always matches the
// verified bytes. image/svg+xml is deliberately not allowed — an SVG can
// embed <script>, which would be a stored-XSS vector if served back as-is.
var allowedContentTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

type Handler struct {
	repo    *Repository
	manager *Manager
	wpRepo  *washingpoint.Repository
}

func NewHandler(repo *Repository, manager *Manager, wpRepo *washingpoint.Repository) *Handler {
	return &Handler{repo: repo, manager: manager, wpRepo: wpRepo}
}

// RegisterRoutes mounts /washing-points/{id}/photos: list is public
// (same as the washing point itself); upload/update/delete require
// requireManage (typically RequireAuth + RequireRole(staff, admin)),
// applied per-method via chi's With().
func (h *Handler) RegisterRoutes(r chi.Router, requireManage ...func(http.Handler) http.Handler) {
	r.Route("/washing-points/{id}/photos", func(wp chi.Router) {
		wp.Get("/", h.list)
		wp.With(requireManage...).Post("/", h.upload)
		wp.With(requireManage...).Patch("/{photoId}", h.update)
		wp.With(requireManage...).Delete("/{photoId}", h.delete)
	})
}

type response struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	IsCover   bool   `json:"is_cover"`
	SortOrder int    `json:"sort_order"`
}

func toResponse(p *WashingPointPhoto) response {
	return response{ID: p.ID.String(), URL: p.URL, IsCover: p.IsCover, SortOrder: p.SortOrder}
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	washingPointID, err := httputil.ParseUUIDParam(r, "id")
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	photos, err := h.repo.ListByWashingPoint(r.Context(), washingPointID)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	items := make([]response, len(photos))
	for i := range photos {
		items[i] = toResponse(&photos[i])
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	authUser, ok := reqctx.AuthUserFromContext(r.Context())
	if !ok {
		httputil.WriteError(w, r, apperror.Unauthorized("unauthenticated", "authentication required"))
		return
	}

	washingPointID, err := httputil.ParseUUIDParam(r, "id")
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

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			httputil.WriteError(w, r, apperror.New(http.StatusRequestEntityTooLarge, "file_too_large", "file exceeds the 10MB limit"))
			return
		}
		httputil.WriteError(w, r, apperror.BadRequest("invalid_file", "could not parse multipart form"))
		return
	}
	defer r.MultipartForm.RemoveAll()

	file, _, err := r.FormFile("file")
	if err != nil {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_file", `file is required (multipart field "file")`))
		return
	}
	defer file.Close()

	sniff := make([]byte, 512)
	n, err := io.ReadFull(file, sniff)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		httputil.WriteError(w, r, apperror.Internal(err))
		return
	}
	sniff = sniff[:n]
	contentType := http.DetectContentType(sniff)
	ext, ok := allowedContentTypes[contentType]
	if !ok {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_content_type", "file must be a JPEG, PNG, GIF or WebP image"))
		return
	}

	isCover := r.FormValue("is_cover") == "true"
	content := io.MultiReader(bytes.NewReader(sniff), file)
	p, err := h.manager.Upload(r.Context(), washingPointID, ext, content, isCover)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, toResponse(p))
}

type updateRequest struct {
	IsCover   *bool `json:"is_cover"`
	SortOrder *int  `json:"sort_order"`
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	authUser, ok := reqctx.AuthUserFromContext(r.Context())
	if !ok {
		httputil.WriteError(w, r, apperror.Unauthorized("unauthenticated", "authentication required"))
		return
	}

	p, err := h.findOwned(r, authUser)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	var req updateRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	if req.IsCover != nil && *req.IsCover {
		if err := h.manager.SetCover(r.Context(), p); err != nil {
			httputil.WriteError(w, r, err)
			return
		}
	}
	if req.SortOrder != nil {
		if *req.SortOrder < 0 {
			httputil.WriteError(w, r, apperror.BadRequest("invalid_sort_order", "sort_order must not be negative"))
			return
		}
		p.SortOrder = *req.SortOrder
		if err := h.repo.Update(r.Context(), p); err != nil {
			httputil.WriteError(w, r, err)
			return
		}
	}
	httputil.WriteJSON(w, http.StatusOK, toResponse(p))
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	authUser, ok := reqctx.AuthUserFromContext(r.Context())
	if !ok {
		httputil.WriteError(w, r, apperror.Unauthorized("unauthenticated", "authentication required"))
		return
	}

	p, err := h.findOwned(r, authUser)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if err := h.manager.Delete(r.Context(), p); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// findOwned resolves both path params ({id}, the washing point, and
// {photoId}) and verifies authUser may act on that washing point, then
// that the photo actually belongs to it — a photo id alone doesn't reveal
// which point it belongs to (same reasoning as
// service.checkOwnsPriceOption), and the two checks together also refuse
// a valid photo id addressed through a *different* point's URL prefix.
func (h *Handler) findOwned(r *http.Request, authUser reqctx.AuthUser) (*WashingPointPhoto, error) {
	washingPointID, err := httputil.ParseUUIDParam(r, "id")
	if err != nil {
		return nil, err
	}
	photoID, err := httputil.ParseUUIDParam(r, "photoId")
	if err != nil {
		return nil, err
	}
	if !authUser.OwnsWashingPoint(washingPointID) {
		return nil, apperror.NotFound("photo_not_found", "photo not found")
	}
	p, err := h.repo.FindByID(r.Context(), photoID)
	if err != nil {
		return nil, err
	}
	if p.WashingPointID != washingPointID {
		return nil, apperror.NotFound("photo_not_found", "photo not found")
	}
	return p, nil
}
