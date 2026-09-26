// Package app builds the fully-wired HTTP handler for the API: every
// feature's repositories/services/handlers, constructed and mounted onto
// one router. It's kept separate from cmd/api so integration tests can
// build the exact same server (via httptest.NewServer(app.New(...))) without
// duplicating this wiring — cmd/api is package main and can't be imported.
package app

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"

	"q-wash-api/internal/admin"
	"q-wash-api/internal/auth"
	"q-wash-api/internal/box"
	"q-wash-api/internal/car"
	"q-wash-api/internal/config"
	"q-wash-api/internal/connectionrequest"
	"q-wash-api/internal/device"
	"q-wash-api/internal/notification"
	"q-wash-api/internal/owner"
	"q-wash-api/internal/photo"
	"q-wash-api/internal/platform/eventbus"
	"q-wash-api/internal/platform/httpserver"
	"q-wash-api/internal/platform/jwt"
	"q-wash-api/internal/platform/push"
	"q-wash-api/internal/platform/sms"
	"q-wash-api/internal/platform/storage"
	"q-wash-api/internal/qrcode"
	"q-wash-api/internal/queue"
	"q-wash-api/internal/schedule"
	"q-wash-api/internal/service"
	"q-wash-api/internal/user"
	"q-wash-api/internal/washingpoint"
)

// New constructs every feature's repositories/services/handlers and mounts
// their routes onto a fresh router, returning the top-level http.Handler.
// Kept in one function so the wiring order and dependency graph between
// features is easy to see as more phases land.
//
// smsSender and fileStorage are injected (rather than constructed
// internally) so tests can pass a spy sender / a temp-dir-backed storage
// instead of the stdout stub / uploads dir cmd/api uses in dev.
func New(database *gorm.DB, cfg config.Config, smsSender sms.Sender, pushSender push.Sender, fileStorage storage.Storage) http.Handler {
	router, v1 := httpserver.NewRouter(database, cfg.HTTP.CORSAllowedOrigins)
	httpserver.MountStatic(router, cfg.Storage.BaseURL, cfg.Storage.Dir)

	jwtManager := jwt.NewManager(cfg.Auth.AccessTokenSecret, cfg.Auth.AccessTokenTTL)

	userRepo := user.NewRepository(database)
	userHandler := user.NewHandler(userRepo)

	authRepo := auth.NewRepository(database)
	authService := auth.NewService(authRepo, userRepo, smsSender, jwtManager, cfg.Auth)
	authHandler := auth.NewHandler(authService, jwtManager)

	wpRepo := washingpoint.NewRepository(database)

	scheduleRepo := schedule.NewRepository(database)
	scheduleManager := schedule.NewManager(scheduleRepo)
	scheduleHandler := schedule.NewHandler(scheduleRepo, scheduleManager, wpRepo)

	queueBus := eventbus.New()

	boxRepo := box.NewRepository(database)
	boxManager := box.NewManager(boxRepo)
	boxHandler := box.NewHandler(boxRepo, boxManager, queueBus)

	wpHandler := washingpoint.NewHandler(wpRepo, scheduleManager, boxManager)

	serviceRepo := service.NewRepository(database)
	serviceManager := service.NewManager(serviceRepo)
	serviceHandler := service.NewHandler(serviceRepo, serviceManager, wpRepo)

	carRepo := car.NewRepository(database)
	carHandler := car.NewHandler(carRepo)

	queueRepo := queue.NewRepository(database)
	queueManager := queue.NewManager(database, queueRepo, carRepo, serviceRepo, wpRepo, scheduleRepo, boxRepo, queueBus)
	queueHandler := queue.NewHandler(queueRepo, queueManager, wpRepo, serviceRepo, userRepo, carRepo, scheduleRepo, boxRepo, queueBus)

	deviceRepo := device.NewRepository(database)
	deviceHandler := device.NewHandler(deviceRepo)

	notificationRepo := notification.NewRepository(database)
	notificationManager := notification.NewManager(notificationRepo, userRepo, queueRepo, deviceRepo, wpRepo, smsSender, pushSender)
	notificationHandler := notification.NewHandler(notificationRepo, notificationManager)
	queueManager.SetStageNotifier(notificationManager)

	ownerRepo := owner.NewRepository(database)
	ownerHandler := owner.NewHandler(ownerRepo)

	connectionRequestRepo := connectionrequest.NewRepository(database)
	connectionRequestManager := connectionrequest.NewManager(connectionRequestRepo, ownerRepo, wpRepo, scheduleManager, boxManager)
	connectionRequestHandler := connectionrequest.NewHandler(connectionRequestRepo, connectionRequestManager)

	adminHandler := admin.NewHandler(wpRepo, ownerRepo, serviceRepo, queueRepo)

	photoRepo := photo.NewRepository(database)
	photoManager := photo.NewManager(photoRepo, fileStorage)
	photoHandler := photo.NewHandler(photoRepo, photoManager, wpRepo)

	qrCodeRepo := qrcode.NewRepository(database)
	qrCodeManager := qrcode.NewManager(qrCodeRepo, wpRepo, queueRepo)
	qrCodeHandler := qrcode.NewHandler(qrCodeRepo, qrCodeManager, wpRepo)

	requireAuth := authHandler.Middleware()
	requireStaff := []func(http.Handler) http.Handler{
		requireAuth,
		auth.RequireRole(string(user.RoleStaff), string(user.RoleAdmin)),
	}
	// requireQueueOps additionally admits worker (docs/PLAN_WEB_APPS.md
	// phase 7) — narrower than requireStaff on purpose: a shift technician
	// gets the live queue/box surface (board, status, pause/resume,
	// live-boxes) but not washingpoint/service/photo/schedule/box
	// management, which stays requireStaff (staff/admin only).
	requireQueueOps := []func(http.Handler) http.Handler{
		requireAuth,
		auth.RequireRole(string(user.RoleStaff), string(user.RoleWorker), string(user.RoleAdmin)),
	}
	// requireAdmin gates the network-wide admin app's own surface (owners,
	// connection requests) — staff are scoped to one point (see
	// user.User.WashingPointID) and have no business managing another
	// point's owner or the onboarding queue.
	requireAdmin := []func(http.Handler) http.Handler{
		requireAuth,
		auth.RequireRole(string(user.RoleAdmin)),
	}

	authHandler.RegisterPublicRoutes(v1)
	wpHandler.RegisterRoutes(v1, requireStaff...)
	serviceHandler.RegisterRoutes(v1, requireStaff...)
	queueHandler.RegisterRoutes(v1, requireAuth, requireStaff, requireQueueOps)
	notificationHandler.RegisterRoutes(v1, requireAuth, requireStaff...)
	ownerHandler.RegisterRoutes(v1, requireAdmin...)
	connectionRequestHandler.RegisterRoutes(v1, requireAdmin...)
	adminHandler.RegisterRoutes(v1, requireAdmin...)
	photoHandler.RegisterRoutes(v1, requireStaff...)
	scheduleHandler.RegisterRoutes(v1, requireStaff...)
	boxHandler.RegisterRoutes(v1, requireStaff...)
	qrCodeHandler.RegisterRoutes(v1, requireAdmin, requireStaff)
	qrCodeHandler.RegisterShortLinkRoutes(router)

	v1.Group(func(protected chi.Router) {
		protected.Use(requireAuth)
		authHandler.RegisterAuthenticatedRoutes(protected)
		userHandler.RegisterRoutes(protected)
		carHandler.RegisterRoutes(protected)
		deviceHandler.RegisterRoutes(protected)
	})

	return router
}

// NewNotificationScheduler builds the background job that sends the
// time-based booking notifications (1h reminder, 5-minute late nudge). It is
// separate from New because it runs alongside the HTTP server, not inside a
// request; cmd/api starts it, tests call Tick directly.
func NewNotificationScheduler(database *gorm.DB, smsSender sms.Sender, pushSender push.Sender, interval time.Duration) *notification.Scheduler {
	notificationRepo := notification.NewRepository(database)
	manager := notification.NewManager(
		notificationRepo,
		user.NewRepository(database),
		queue.NewRepository(database),
		device.NewRepository(database),
		washingpoint.NewRepository(database),
		smsSender,
		pushSender,
	)
	return notification.NewScheduler(notificationRepo, manager, interval)
}
