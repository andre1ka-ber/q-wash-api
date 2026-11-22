// Package platformsettings holds the handful of platform-wide rules the
// admin app edits: how far ahead customers may book, when an unarrived
// booking is marked no-show, how late a customer may still cancel, whether
// a booking ticket is sent by SMS, and whether new points wait for admin
// review. One row in platform_settings; every consumer reads it per use, so
// a change applies at once without a restart.
package platformsettings

import (
	"context"
	"net/http"
	"time"

	"gorm.io/gorm"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/httputil"
)

const (
	MaxHorizonDays      = 60
	MaxNoShowMinutes    = 240
	MaxCancelCutoffMins = 1440
)

// Settings is the single platform_settings row.
type Settings struct {
	BookingHorizonDays    int       `gorm:"column:booking_horizon_days" json:"booking_horizon_days"`
	NoShowAutoCancelMin   int       `gorm:"column:no_show_auto_cancel_min" json:"no_show_auto_cancel_min"`
	ClientCancelCutoffMin int       `gorm:"column:client_cancel_cutoff_min" json:"client_cancel_cutoff_min"`
	SMSConfirmation       bool      `gorm:"column:sms_confirmation" json:"sms_confirmation"`
	NewPointModeration    bool      `gorm:"column:new_point_moderation" json:"new_point_moderation"`
	UpdatedAt             time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (Settings) TableName() string { return "platform_settings" }

// Defaults mirror the migration; used when the row is missing (never in a
// migrated database) so callers never have to special-case it.
func Defaults() Settings {
	return Settings{BookingHorizonDays: 7, NoShowAutoCancelMin: 0, ClientCancelCutoffMin: 0, SMSConfirmation: false, NewPointModeration: true}
}

// Provider is what other packages depend on to read the settings.
type Provider interface {
	Get(ctx context.Context) (Settings, error)
}

type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository { return &Repository{db: db} }

func (r *Repository) Get(ctx context.Context) (Settings, error) {
	var s Settings
	err := r.db.WithContext(ctx).Where("id = 1").Take(&s).Error
	if err == gorm.ErrRecordNotFound {
		return Defaults(), nil
	}
	if err != nil {
		return Settings{}, apperror.Internal(err)
	}
	return s, nil
}

// Update is a partial update: only the non-nil fields change.
type Update struct {
	BookingHorizonDays    *int  `json:"booking_horizon_days"`
	NoShowAutoCancelMin   *int  `json:"no_show_auto_cancel_min"`
	ClientCancelCutoffMin *int  `json:"client_cancel_cutoff_min"`
	SMSConfirmation       *bool `json:"sms_confirmation"`
	NewPointModeration    *bool `json:"new_point_moderation"`
}

// Validate rejects out-of-range values with a field-specific 400.
func (u Update) Validate() error {
	if v := u.BookingHorizonDays; v != nil && (*v < 1 || *v > MaxHorizonDays) {
		return apperror.BadRequest("invalid_booking_horizon_days", "booking_horizon_days must be between 1 and 60")
	}
	if v := u.NoShowAutoCancelMin; v != nil && (*v < 0 || *v > MaxNoShowMinutes) {
		return apperror.BadRequest("invalid_no_show_auto_cancel_min", "no_show_auto_cancel_min must be between 0 (off) and 240")
	}
	if v := u.ClientCancelCutoffMin; v != nil && (*v < 0 || *v > MaxCancelCutoffMins) {
		return apperror.BadRequest("invalid_client_cancel_cutoff_min", "client_cancel_cutoff_min must be between 0 (off) and 1440")
	}
	return nil
}

func (u Update) empty() bool {
	return u.BookingHorizonDays == nil && u.NoShowAutoCancelMin == nil && u.ClientCancelCutoffMin == nil &&
		u.SMSConfirmation == nil && u.NewPointModeration == nil
}

func (r *Repository) Apply(ctx context.Context, u Update) (Settings, error) {
	updates := map[string]any{"updated_at": time.Now()}
	if u.BookingHorizonDays != nil {
		updates["booking_horizon_days"] = *u.BookingHorizonDays
	}
	if u.NoShowAutoCancelMin != nil {
		updates["no_show_auto_cancel_min"] = *u.NoShowAutoCancelMin
	}
	if u.ClientCancelCutoffMin != nil {
		updates["client_cancel_cutoff_min"] = *u.ClientCancelCutoffMin
	}
	if u.SMSConfirmation != nil {
		updates["sms_confirmation"] = *u.SMSConfirmation
	}
	if u.NewPointModeration != nil {
		updates["new_point_moderation"] = *u.NewPointModeration
	}
	if err := r.db.WithContext(ctx).Model(&Settings{}).Where("id = 1").Updates(updates).Error; err != nil {
		return Settings{}, apperror.Internal(err)
	}
	return r.Get(ctx)
}

type Handler struct {
	repo *Repository
}

func NewHandler(repo *Repository) *Handler { return &Handler{repo: repo} }

// Get and Patch are mounted by internal/admin under /admin/settings (admin-only),
// next to the other admin routes — chi cannot mount a second router on the
// same /admin prefix.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	s, err := h.repo.Get(r.Context())
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, s)
}

func (h *Handler) Patch(w http.ResponseWriter, r *http.Request) {
	var in Update
	if err := httputil.DecodeJSON(r, &in); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if in.empty() {
		httputil.WriteError(w, r, apperror.BadRequest("empty_update", "no settings to change"))
		return
	}
	if err := in.Validate(); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	s, err := h.repo.Apply(r.Context(), in)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, s)
}
