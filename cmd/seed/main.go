// Command seed populates the local database with baseline dev/demo data:
// one washing point, a couple of services with price options, an admin,
// a staff member, a worker, three demo customers with cars, and two non-overlapping
// queue bookings (on two different customers — a customer can only have
// one active booking at a time, so demonstrating "both boxes busy at once"
// needs two accounts, not one booking each on the same account). It is
// idempotent (safe to re-run) and finishes with a self-check that the
// DB-level overlap-prevention constraint on `queue` actually rejects a
// double-booked box.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"q-wash-api/internal/box"
	"q-wash-api/internal/car"
	"q-wash-api/internal/config"
	"q-wash-api/internal/platform/db"
	"q-wash-api/internal/queue"
	"q-wash-api/internal/schedule"
	"q-wash-api/internal/service"
	"q-wash-api/internal/user"
	"q-wash-api/internal/washingpoint"
)

func main() {
	if err := run(); err != nil {
		slog.Error("seed failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	_ = godotenv.Load()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	database, err := db.Connect(cfg.DB, gormlogger.Default.LogMode(gormlogger.Warn))
	if err != nil {
		return err
	}
	sqlDB, err := database.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	wp, err := seedWashingPoint(database)
	if err != nil {
		return fmt.Errorf("seed washing point: %w", err)
	}
	slog.Info("washing point ready", "id", wp.ID, "name", wp.Name, "boxes", wp.BoxesCount)

	// The availability algorithm reads washing_point_schedules exclusively
	// as of docs/PLAN_WEB_APPS.md phase 5 — a point with no schedule rows
	// is treated as closed every day (see queue.resolveDaySchedule), so
	// seed the same every-day-08:00-20:00 default that
	// washingpoint.Handler.create and connectionrequest.Manager.Approve
	// auto-provision going forward. Idempotent: ReplaceAll always
	// delete-then-inserts, so re-running the seed just rewrites the same 7
	// rows rather than accumulating duplicates.
	scheduleManager := schedule.NewManager(schedule.NewRepository(database))
	if err := scheduleManager.SeedDefault(context.Background(), wp.ID, wp.OpenTime, wp.CloseTime); err != nil {
		return fmt.Errorf("seed washing point schedule: %w", err)
	}
	slog.Info("washing point schedule ready", "washing_point_id", wp.ID)

	// Same reasoning as the schedule seed above: cmd/seed builds the
	// washing point via raw GORM, bypassing washingpoint.Handler.create's
	// own box.Manager.SeedDefault call entirely. Unlike the schedule seed,
	// box.Manager.SeedDefault is not idempotent (no ReplaceAll-style
	// delete-then-insert — see its own doc comment), so this checks for an
	// existing row first rather than calling it unconditionally on every
	// re-run, which would violate the boxes UNIQUE(washing_point_id, number)
	// constraint on the second run.
	boxRepo := box.NewRepository(database)
	existingBoxes, err := boxRepo.ListByWashingPoint(context.Background(), wp.ID)
	if err != nil {
		return fmt.Errorf("check existing boxes: %w", err)
	}
	if len(existingBoxes) == 0 {
		if err := box.NewManager(boxRepo).SeedDefault(context.Background(), wp.ID, wp.BoxesCount); err != nil {
			return fmt.Errorf("seed washing point boxes: %w", err)
		}
	}
	slog.Info("washing point boxes ready", "washing_point_id", wp.ID, "count", wp.BoxesCount)

	fullWash, err := seedService(database, wp.ID, "Full wash", 90, []priceOptionSeed{
		{Name: "Sedan", PriceCents: 1500, IsDefault: true},
		{Name: "SUV", PriceCents: 2000},
	})
	if err != nil {
		return fmt.Errorf("seed full wash service: %w", err)
	}
	slog.Info("service ready", "id", fullWash.ID, "name", fullWash.Name)

	expressWash, err := seedService(database, wp.ID, "Express wash", 30, []priceOptionSeed{
		{Name: "Standard", PriceCents: 800, IsDefault: true},
	})
	if err != nil {
		return fmt.Errorf("seed express wash service: %w", err)
	}
	slog.Info("service ready", "id", expressWash.ID, "name", expressWash.Name)

	admin, err := seedUser(database, "+15550000001", strPtr("Admin"), user.RoleAdmin)
	if err != nil {
		return fmt.Errorf("seed admin: %w", err)
	}
	staff, err := seedUser(database, "+15550000002", strPtr("Staff"), user.RoleStaff)
	if err != nil {
		return fmt.Errorf("seed staff: %w", err)
	}
	// A worker account, needed for q-wash-worker (username/password login,
	// same as staff/admin) to have anything to log in against locally.
	// Real usage is still blocked on PLAN_WEB_APPS.md phase 7 (the
	// requireStaff RBAC middleware only allows staff/admin today, so this
	// account can log in and call GET /me but 403s on every management
	// route) — seeded now anyway so q-wash-worker's auth flow itself is
	// testable end to end ahead of that phase landing.
	worker, err := seedUser(database, "+15550000006", strPtr("Азиз Каримов"), user.RoleWorker)
	if err != nil {
		return fmt.Errorf("seed worker: %w", err)
	}
	customer, err := seedUser(database, "+15550000003", strPtr("Demo Customer"), user.RoleCustomer)
	if err != nil {
		return fmt.Errorf("seed customer: %w", err)
	}
	// A second customer for the box-2 booking below, and a third solely for
	// the overlap self-check — a customer can only have one active booking
	// at a time (queue_one_active_booking_per_user), so the "both boxes
	// busy" demo scenario and the box-overlap self-check each need an
	// account with no other active booking of their own.
	customer2, err := seedUser(database, "+15550000004", strPtr("Demo Customer 2"), user.RoleCustomer)
	if err != nil {
		return fmt.Errorf("seed customer 2: %w", err)
	}
	customer3, err := seedUser(database, "+15550000005", strPtr("Demo Customer 3"), user.RoleCustomer)
	if err != nil {
		return fmt.Errorf("seed customer 3: %w", err)
	}
	slog.Info("users ready", "admin", admin.ID, "staff", staff.ID, "worker", worker.ID, "customer", customer.ID, "customer2", customer2.ID, "customer3", customer3.ID)

	// Username/password login is staff/admin-only (customers stay
	// phone+OTP) — used by the queue board and staff panel. Dev-only
	// defaults, deliberately not secrets: this is local seed data.
	if err := setStaffCredentials(database, admin.ID, "admin", "admin12345"); err != nil {
		return fmt.Errorf("set admin credentials: %w", err)
	}
	if err := setStaffCredentials(database, staff.ID, "staff", "staff12345"); err != nil {
		return fmt.Errorf("set staff credentials: %w", err)
	}
	if err := setStaffCredentials(database, worker.ID, "worker", "worker12345"); err != nil {
		return fmt.Errorf("set worker credentials: %w", err)
	}
	slog.Info("staff/admin/worker login credentials ready", "admin_username", "admin", "staff_username", "staff", "worker_username", "worker")

	// Scopes the seeded staff account to the seeded washing point — needed
	// for q-wash-cabinet (staff-only, no point picker) to have anything to
	// log in against locally. seedUser's FirstOrCreate only applies this on
	// first insert (Attrs), so a plain Update here keeps it idempotent
	// across re-runs, same pattern as setStaffCredentials.
	if err := database.Model(&user.User{}).Where("id = ?", staff.ID).
		Update("washing_point_id", wp.ID).Error; err != nil {
		return fmt.Errorf("set staff washing_point_id: %w", err)
	}
	// Same for the worker account — q-wash-worker's ProtectedRoute requires
	// washing_point_id just like q-wash-cabinet's does.
	if err := database.Model(&user.User{}).Where("id = ?", worker.ID).
		Update("washing_point_id", wp.ID).Error; err != nil {
		return fmt.Errorf("set worker washing_point_id: %w", err)
	}

	demoCar, err := seedCar(database, customer.ID, "Demo Car")
	if err != nil {
		return fmt.Errorf("seed car: %w", err)
	}
	demoCar2, err := seedCar(database, customer2.ID, "Demo Car 2")
	if err != nil {
		return fmt.Errorf("seed car 2: %w", err)
	}
	demoCar3, err := seedCar(database, customer3.ID, "Demo Car 3")
	if err != nil {
		return fmt.Errorf("seed car 3: %w", err)
	}
	slog.Info("cars ready", "id", demoCar.ID, "name", demoCar.Name, "id2", demoCar2.ID, "id3", demoCar3.ID)

	tomorrow := time.Now().UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
	start1 := tomorrow.Add(10 * time.Hour)
	if _, err := seedQueueEntry(database, customer.ID, demoCar.ID, fullWash.ID, fullWash.PriceOptions[0].ID, wp.ID, 1, start1, 90*time.Minute); err != nil {
		return fmt.Errorf("seed queue entry (box 1): %w", err)
	}
	start2 := tomorrow.Add(10*time.Hour + 30*time.Minute)
	if _, err := seedQueueEntry(database, customer2.ID, demoCar2.ID, expressWash.ID, expressWash.PriceOptions[0].ID, wp.ID, 2, start2, 30*time.Minute); err != nil {
		return fmt.Errorf("seed queue entry (box 2): %w", err)
	}
	slog.Info("demo queue bookings ready", "box1_start", start1, "box2_start", start2)

	if err := verifyOverlapConstraint(database, customer3.ID, demoCar3.ID, fullWash.ID, fullWash.PriceOptions[0].ID, wp.ID, start1); err != nil {
		return fmt.Errorf("overlap constraint self-check: %w", err)
	}
	slog.Info("overlap constraint self-check passed: DB rejected a double-booked box as expected")

	slog.Info("seed complete")
	return nil
}

type priceOptionSeed struct {
	Name       string
	PriceCents int
	IsDefault  bool
}

func seedWashingPoint(database *gorm.DB) (washingpoint.WashingPoint, error) {
	var wp washingpoint.WashingPoint
	err := database.Where(washingpoint.WashingPoint{Name: "Pegasus Wash - Downtown"}).
		Attrs(washingpoint.WashingPoint{
			Address:    "1 Main St",
			Latitude:   50.4501,
			Longitude:  30.5234,
			BoxesCount: 2,
			OpenTime:   "08:00",
			CloseTime:  "20:00",
			Status:     washingpoint.StatusActive,
		}).
		FirstOrCreate(&wp).Error
	return wp, err
}

func seedService(database *gorm.DB, washingPointID uuid.UUID, name string, durationMinutes int, prices []priceOptionSeed) (service.Service, error) {
	var svc service.Service
	err := database.
		Where(service.Service{WashingPointID: washingPointID, Name: name}).
		Attrs(service.Service{DurationMinutes: durationMinutes, IsActive: true}).
		FirstOrCreate(&svc).Error
	if err != nil {
		return service.Service{}, err
	}

	if err := database.Where("service_id = ?", svc.ID).Find(&svc.PriceOptions).Error; err != nil {
		return service.Service{}, err
	}
	if len(svc.PriceOptions) == 0 {
		for _, p := range prices {
			opt := service.ServicePriceOption{
				ServiceID:  svc.ID,
				Name:       p.Name,
				PriceCents: p.PriceCents,
				IsDefault:  p.IsDefault,
			}
			if err := database.Create(&opt).Error; err != nil {
				return service.Service{}, err
			}
			svc.PriceOptions = append(svc.PriceOptions, opt)
		}
	}

	return svc, nil
}

func seedUser(database *gorm.DB, phone string, name *string, role user.Role) (user.User, error) {
	var u user.User
	err := database.Where(user.User{PhoneNumber: phone}).
		Attrs(user.User{Name: name, Role: role}).
		FirstOrCreate(&u).Error
	return u, err
}

func setStaffCredentials(database *gorm.DB, userID uuid.UUID, username, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return database.Model(&user.User{}).Where("id = ?", userID).Updates(map[string]any{
		"username":      username,
		"password_hash": string(hash),
	}).Error
}

func seedCar(database *gorm.DB, userID uuid.UUID, name string) (car.Car, error) {
	var c car.Car
	err := database.Where(car.Car{UserID: userID, Name: name}).
		FirstOrCreate(&c).Error
	return c, err
}

func seedQueueEntry(
	database *gorm.DB,
	userID, carID, serviceID, priceOptionID, washingPointID uuid.UUID,
	boxNumber int,
	startAt time.Time,
	duration time.Duration,
) (queue.Queue, error) {
	var q queue.Queue
	err := database.Where(queue.Queue{
		WashingPointID: washingPointID,
		BoxNumber:      boxNumber,
	}).Where("scheduled_start_at = ?", startAt).
		Attrs(queue.Queue{
			Status:           queue.StatusQueue,
			UserID:           userID,
			CarID:            carID,
			ServiceID:        serviceID,
			PriceOptionID:    priceOptionID,
			ScheduledStartAt: startAt,
			ScheduledEndAt:   startAt.Add(duration),
		}).
		FirstOrCreate(&q).Error
	return q, err
}

// verifyOverlapConstraint attempts to insert a queue entry that overlaps an
// existing booking on the same box, inside a transaction that always rolls
// back. It fails loudly if the insert unexpectedly succeeds.
func verifyOverlapConstraint(
	database *gorm.DB,
	userID, carID, serviceID, priceOptionID, washingPointID uuid.UUID,
	conflictingStart time.Time,
) error {
	rollbackSentinel := errors.New("rollback: self-check complete")

	// The constraint violation below is the expected outcome of this check,
	// not a real error — silence the logger so it doesn't print a scary
	// ERROR line during a normal seed run.
	quiet := database.Session(&gorm.Session{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})

	txErr := quiet.Transaction(func(tx *gorm.DB) error {
		overlapping := queue.Queue{
			Status:           queue.StatusQueue,
			UserID:           userID,
			CarID:            carID,
			ServiceID:        serviceID,
			PriceOptionID:    priceOptionID,
			WashingPointID:   washingPointID,
			BoxNumber:        1,
			ScheduledStartAt: conflictingStart.Add(15 * time.Minute),
			ScheduledEndAt:   conflictingStart.Add(45 * time.Minute),
		}
		insertErr := tx.Create(&overlapping).Error
		if insertErr == nil {
			return fmt.Errorf("expected the DB to reject an overlapping booking on box 1, but it succeeded")
		}
		// Any DB error here is the expected outcome; roll back and report success.
		return rollbackSentinel
	})

	if errors.Is(txErr, rollbackSentinel) {
		return nil
	}
	return txErr
}

func strPtr(s string) *string { return &s }
