// Command seed-demo populates a LOCAL database with presentation-ready demo
// data: two washing points (owners, boxes, per-weekday schedules, services
// with price options), admin/staff/worker logins, a pool of customers with
// cars, a live "today" queue covering every status (queue, waiting, washing,
// paused washing, ready, no_show, canceled, manual walk-in, QR), a few
// bookings for tomorrow, and connection requests for the admin inbox.
//
// It is complementary to cmd/seed (baseline dev data) and can run on top of
// it. Re-running is safe: static rows are found-or-created, and the demo
// points' queue is wiped and rebuilt relative to "now" so the timeline is
// always fresh right before a presentation.
//
// It refuses to run against APP_ENV=production or a non-local DB host.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"q-wash-api/internal/box"
	"q-wash-api/internal/car"
	"q-wash-api/internal/config"
	"q-wash-api/internal/connectionrequest"
	"q-wash-api/internal/owner"
	"q-wash-api/internal/platform/db"
	"q-wash-api/internal/queue"
	"q-wash-api/internal/schedule"
	"q-wash-api/internal/service"
	"q-wash-api/internal/user"
	"q-wash-api/internal/washingpoint"
)

const (
	demoPassword = "demo12345"
	adminLogin   = "demo-admin"
)

type priceSeed struct {
	Name      string
	Smn       int
	IsDefault bool
}

type serviceSeed struct {
	Name    string
	Minutes int
	Prices  []priceSeed
	Desc    string
}

var demoServices = []serviceSeed{
	{Name: "Экспресс-мойка", Minutes: 30, Desc: "Быстрая мойка кузова снаружи", Prices: []priceSeed{
		{"Седан", 50, true}, {"Внедорожник", 70, false},
	}},
	{Name: "Комплексная мойка", Minutes: 60, Desc: "Кузов, коврики, пылесос и протирка салона", Prices: []priceSeed{
		{"Седан", 100, true}, {"Внедорожник", 140, false},
	}},
	{Name: "Химчистка салона", Minutes: 120, Desc: "Глубокая чистка сидений и потолка", Prices: []priceSeed{
		{"Стандарт", 250, true},
	}},
}

type pointSeed struct {
	Owner     string
	Name      string
	Address   string
	Lat, Lng  float64
	Boxes     int
	StaffName string
	StaffUser string
	WorkName  string
	WorkUser  string
	Desc      string
	Amenities []string
}

var demoPoints = []pointSeed{
	{
		Owner: "ИП Рахимов", Name: "Q-Wash Центр", Address: "пр. Рудаки, 75, Душанбе",
		Lat: 38.5598, Lng: 68.7870, Boxes: 3,
		StaffName: "Фарход Рахимов", StaffUser: "demo-staff1",
		WorkName: "Азиз Каримов", WorkUser: "demo-worker1",
		Desc:      "Три бокса в центре города, мойка без ожидания.",
		Amenities: []string{"Wi-Fi", "Кофе", "Зона ожидания"},
	},
	{
		Owner: "ООО Мастер-Мойка", Name: "Q-Wash Сомони", Address: "ул. Айни, 24, Душанбе",
		Lat: 38.5737, Lng: 68.7738, Boxes: 2,
		StaffName: "Достон Назаров", StaffUser: "demo-staff2",
		WorkName: "Рустам Хакимов", WorkUser: "demo-worker2",
		Desc:      "Два бокса, работаем ежедневно.",
		Amenities: []string{"Кофе", "Пылесос самообслуживания"},
	},
}

type customerSeed struct {
	Phone string
	Name  string
	Car   string
	Plate string
}

// Index matters: bookings below refer to customers by position. Index 13 is
// the live-demo client and intentionally has no booking.
var demoCustomers = []customerSeed{
	{"+992900000001", "Шерзод Алиев", "Toyota Camry", "0123 AA 01"},
	{"+992900000002", "Мадина Юсупова", "Hyundai Elantra", "4521 BC 01"},
	{"+992900000003", "Бахтиёр Саидов", "Chevrolet Cobalt", "7788 CD 02"},
	{"+992900000004", "Нигина Ходжаева", "Kia Sportage", "3310 AB 01"},
	{"+992900000005", "Фаррух Исмоилов", "Lexus RX 350", "9001 EE 01"},
	{"+992900000006", "Зарина Мирзоева", "Daewoo Matiz", "1212 AA 03"},
	{"+992900000007", "Умед Расулов", "Mercedes E200", "5555 KM 01"},
	{"+992900000008", "Дилором Бобоева", "Toyota Prius", "2468 BC 01"},
	{"+992900000009", "Ёдгор Сафаров", "Nissan X-Trail", "1357 CD 02"},
	{"+992900000010", "Сарвар Курбонов", "Honda CR-V", "8642 AB 01"},
	{"+992900000011", "Гулнора Рашидова", "Opel Astra", "7410 EE 01"},
	{"+992900000012", "Тимур Холиков", "BMW X5", "9630 KM 01"},
	{"+992900000013", "Парвиз Ятимов", "Chevrolet Lacetti", "1470 AA 01"},
	{"+992900000100", "Демо Клиент", "Toyota Corolla", "0001 DM 01"},
}

// booking is one queue row. Offsets are minutes relative to the anchor time.
type booking struct {
	Point    int // index into demoPoints
	Box      int
	Start    int // minutes from anchor
	Svc      int // index into demoServices
	Price    int // index into the service's Prices
	Cust     int // index into demoCustomers
	Status   queue.Status
	Source   queue.Source
	NoCar    bool
	Paused   bool
	Notes    string
	Tomorrow *int // if set, Start is ignored: minutes after local 00:00 tomorrow
}

func tomorrowAt(h, m int) *int { v := h*60 + m; return &v }

var demoBookings = []booking{
	// Point 0, box 1
	{Point: 0, Box: 1, Start: -150, Svc: 1, Cust: 6, Status: queue.StatusReady, Source: queue.SourceApp},
	{Point: 0, Box: 1, Start: -30, Svc: 1, Cust: 0, Status: queue.StatusWashing, Source: queue.SourceApp},
	{Point: 0, Box: 1, Start: 45, Svc: 0, Cust: 1, Status: queue.StatusQueue, Source: queue.SourceApp},
	// Point 0, box 2
	{Point: 0, Box: 2, Start: -60, Svc: 0, Cust: 7, Status: queue.StatusReady, Source: queue.SourceApp},
	{Point: 0, Box: 2, Start: 0, Svc: 0, Cust: 2, Status: queue.StatusWaiting, Source: queue.SourceManual, NoCar: true, Notes: "Клиент без записи, приехал на месте"},
	{Point: 0, Box: 2, Start: 60, Svc: 2, Cust: 3, Status: queue.StatusQueue, Source: queue.SourceQR},
	// Point 0, box 3
	{Point: 0, Box: 3, Start: -120, Svc: 0, Cust: 8, Status: queue.StatusNoShow, Source: queue.SourceApp},
	{Point: 0, Box: 3, Start: -15, Svc: 2, Cust: 4, Status: queue.StatusWashing, Source: queue.SourceApp, Paused: true, Notes: "Пауза: клиент попросил доплатить за воск"},
	{Point: 0, Box: 3, Start: 30, Svc: 0, Cust: 9, Status: queue.StatusCanceled, Source: queue.SourceApp},
	{Point: 0, Box: 3, Start: 120, Svc: 0, Cust: 5, Status: queue.StatusQueue, Source: queue.SourceApp},
	// Point 1
	{Point: 1, Box: 1, Start: -20, Svc: 0, Cust: 10, Status: queue.StatusWashing, Source: queue.SourceApp},
	{Point: 1, Box: 1, Start: 30, Svc: 1, Cust: 11, Status: queue.StatusQueue, Source: queue.SourceApp},
	{Point: 1, Box: 2, Start: 0, Svc: 0, Cust: 12, Status: queue.StatusWaiting, Source: queue.SourceApp},
	// Tomorrow, point 0
	{Point: 0, Box: 1, Svc: 1, Cust: 6, Status: queue.StatusQueue, Source: queue.SourceApp, Tomorrow: tomorrowAt(10, 0)},
	{Point: 0, Box: 2, Svc: 0, Cust: 7, Status: queue.StatusQueue, Source: queue.SourceApp, Tomorrow: tomorrowAt(10, 0)},
	{Point: 0, Box: 1, Svc: 2, Cust: 8, Status: queue.StatusQueue, Source: queue.SourceApp, Tomorrow: tomorrowAt(11, 30)},
}

func main() {
	if err := run(); err != nil {
		slog.Error("seed-demo failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	_ = godotenv.Load()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.IsProduction() {
		return fmt.Errorf("refusing to seed demo data with APP_ENV=production")
	}
	if h := cfg.DB.Host; h != "localhost" && h != "127.0.0.1" && h != "::1" {
		return fmt.Errorf("refusing to seed demo data into non-local DB host %q", h)
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

	ctx := context.Background()
	loc, err := time.LoadLocation("Asia/Dushanbe")
	if err != nil {
		return fmt.Errorf("load Asia/Dushanbe: %w", err)
	}
	anchor, dayStartTomorrow := timeline(time.Now().In(loc), loc)
	slog.Info("timeline anchor", "anchor", anchor.Format("2006-01-02 15:04 MST"))
	if d := time.Since(anchor); d > 15*time.Minute || d < -15*time.Minute {
		slog.Warn("real clock is outside 11:00-17:00 Asia/Dushanbe, so the queue is pinned to 13:00 and live views (display progress bars, 'уже пора') will not match the wall clock; re-run `make seed-demo` between 11:00 and 17:00 right before presenting")
	}

	// Customers and cars.
	custIDs := make([]uuid.UUID, len(demoCustomers))
	carIDs := make([]uuid.UUID, len(demoCustomers))
	for i, c := range demoCustomers {
		u, err := findOrCreateCustomer(database, c)
		if err != nil {
			return fmt.Errorf("customer %s: %w", c.Phone, err)
		}
		custIDs[i] = u.ID
		var cr car.Car
		if err := database.Where(car.Car{UserID: u.ID, Name: c.Car}).
			Attrs(car.Car{Plate: &c.Plate}).FirstOrCreate(&cr).Error; err != nil {
			return fmt.Errorf("car for %s: %w", c.Phone, err)
		}
		carIDs[i] = cr.ID
	}
	slog.Info("customers ready", "count", len(demoCustomers))

	// Platform admin.
	if _, err := upsertLogin(database, adminLogin, "Демо Админ", user.RoleAdmin, nil); err != nil {
		return fmt.Errorf("admin: %w", err)
	}

	// Points and everything hanging off them.
	pointIDs := make([]uuid.UUID, len(demoPoints))
	svcIDs := make([][]uuid.UUID, len(demoPoints))
	priceIDs := make([][][]uuid.UUID, len(demoPoints))
	for i, p := range demoPoints {
		wp, err := seedPoint(ctx, database, p)
		if err != nil {
			return fmt.Errorf("point %q: %w", p.Name, err)
		}
		pointIDs[i] = wp.ID
		svcIDs[i], priceIDs[i], err = seedServices(database, wp.ID)
		if err != nil {
			return fmt.Errorf("services for %q: %w", p.Name, err)
		}
		if _, err := upsertLogin(database, p.StaffUser, p.StaffName, user.RoleStaff, &wp.ID); err != nil {
			return fmt.Errorf("staff for %q: %w", p.Name, err)
		}
		if _, err := upsertLogin(database, p.WorkUser, p.WorkName, user.RoleWorker, &wp.ID); err != nil {
			return fmt.Errorf("worker for %q: %w", p.Name, err)
		}
		slog.Info("point ready", "name", wp.Name, "id", wp.ID, "boxes", wp.BoxesCount)
	}

	// Rebuild the demo points' queue from scratch.
	if err := database.Where("washing_point_id IN ?", pointIDs).Delete(&queue.Queue{}).Error; err != nil {
		return fmt.Errorf("clear demo queue: %w", err)
	}
	now := time.Now()
	for n, b := range demoBookings {
		svc := demoServices[b.Svc]
		start := anchor.Add(time.Duration(b.Start) * time.Minute)
		if b.Tomorrow != nil {
			start = dayStartTomorrow.Add(time.Duration(*b.Tomorrow) * time.Minute)
		}
		q := queue.Queue{
			Status:           b.Status,
			UserID:           custIDs[b.Cust],
			ServiceID:        svcIDs[b.Point][b.Svc],
			PriceOptionID:    priceIDs[b.Point][b.Svc][b.Price],
			WashingPointID:   pointIDs[b.Point],
			BoxNumber:        b.Box,
			ScheduledStartAt: start.UTC(),
			ScheduledEndAt:   start.Add(time.Duration(svc.Minutes) * time.Minute).UTC(),
			Source:           b.Source,
		}
		if !b.NoCar {
			c := carIDs[b.Cust]
			q.CarID = &c
		}
		if b.Notes != "" {
			notes := b.Notes
			q.Notes = &notes
		}
		if b.Status == queue.StatusCanceled {
			t := now.Add(-10 * time.Minute)
			q.CanceledAt = &t
		}
		if b.Paused {
			t := now.Add(-5 * time.Minute)
			q.PausedAt = &t
		}
		if err := database.Create(&q).Error; err != nil {
			return fmt.Errorf("booking #%d (%s box %d): %w", n, demoPoints[b.Point].Name, b.Box, err)
		}
	}
	slog.Info("demo queue rebuilt", "bookings", len(demoBookings))

	if err := seedConnectionRequests(database); err != nil {
		return fmt.Errorf("connection requests: %w", err)
	}

	printSummary()
	return nil
}

// timeline picks the "now" the demo queue is laid out around. Offsets reach
// roughly -3h..+3h, so a real now outside 11:00-16:59 local would push rows
// outside the 08:00-20:00 operating window; pin to 13:00 local then.
func timeline(nowLocal time.Time, loc *time.Location) (anchor, tomorrowStart time.Time) {
	y, m, d := nowLocal.Date()
	if h := nowLocal.Hour(); h >= 11 && h < 17 {
		anchor = nowLocal.Truncate(15 * time.Minute)
	} else {
		anchor = time.Date(y, m, d, 13, 0, 0, 0, loc)
	}
	tomorrowStart = time.Date(y, m, d+1, 0, 0, 0, 0, loc)
	return anchor, tomorrowStart
}

func findOrCreateCustomer(database *gorm.DB, c customerSeed) (user.User, error) {
	var u user.User
	phone, name := c.Phone, c.Name
	err := database.Where("phone_number = ?", phone).
		Attrs(user.User{PhoneNumber: &phone, Name: &name, Role: user.RoleCustomer}).
		FirstOrCreate(&u).Error
	return u, err
}

// upsertLogin creates (or refreshes) a username+password account.
func upsertLogin(database *gorm.DB, username, name string, role user.Role, pointID *uuid.UUID) (user.User, error) {
	var u user.User
	uname, dname := username, name
	if err := database.Where("username = ?", username).
		Attrs(user.User{Username: &uname, Name: &dname, Role: role}).
		FirstOrCreate(&u).Error; err != nil {
		return u, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(demoPassword), bcrypt.DefaultCost)
	if err != nil {
		return u, err
	}
	err = database.Model(&user.User{}).Where("id = ?", u.ID).Updates(map[string]any{
		"password_hash":    string(hash),
		"washing_point_id": pointID,
	}).Error
	return u, err
}

func seedPoint(ctx context.Context, database *gorm.DB, p pointSeed) (washingpoint.WashingPoint, error) {
	var own owner.Owner
	if err := database.Where(owner.Owner{Name: p.Owner}).FirstOrCreate(&own).Error; err != nil {
		return washingpoint.WashingPoint{}, fmt.Errorf("owner: %w", err)
	}

	desc := p.Desc
	var wp washingpoint.WashingPoint
	err := database.Where(washingpoint.WashingPoint{Name: p.Name}).
		Attrs(washingpoint.WashingPoint{
			OwnerID:     &own.ID,
			Address:     p.Address,
			Latitude:    p.Lat,
			Longitude:   p.Lng,
			BoxesCount:  p.Boxes,
			OpenTime:    "08:00",
			CloseTime:   "20:00",
			Status:      washingpoint.StatusActive,
			Description: &desc,
			Amenities:   pq.StringArray(p.Amenities),
		}).FirstOrCreate(&wp).Error
	if err != nil {
		return wp, err
	}

	if err := schedule.NewManager(schedule.NewRepository(database)).
		SeedDefault(ctx, wp.ID, wp.OpenTime, wp.CloseTime); err != nil {
		return wp, fmt.Errorf("schedule: %w", err)
	}

	boxRepo := box.NewRepository(database)
	existing, err := boxRepo.ListByWashingPoint(ctx, wp.ID)
	if err != nil {
		return wp, fmt.Errorf("list boxes: %w", err)
	}
	if len(existing) == 0 {
		if err := box.NewManager(boxRepo).SeedDefault(ctx, wp.ID, wp.BoxesCount); err != nil {
			return wp, fmt.Errorf("boxes: %w", err)
		}
	}
	return wp, nil
}

// seedServices returns service IDs and, per service, price-option IDs in
// the order declared in demoServices.
func seedServices(database *gorm.DB, pointID uuid.UUID) ([]uuid.UUID, [][]uuid.UUID, error) {
	svcIDs := make([]uuid.UUID, len(demoServices))
	priceIDs := make([][]uuid.UUID, len(demoServices))
	for i, s := range demoServices {
		desc := s.Desc
		var svc service.Service
		if err := database.Where(service.Service{WashingPointID: pointID, Name: s.Name}).
			Attrs(service.Service{DurationMinutes: s.Minutes, Description: &desc, IsActive: true}).
			FirstOrCreate(&svc).Error; err != nil {
			return nil, nil, err
		}
		svcIDs[i] = svc.ID
		priceIDs[i] = make([]uuid.UUID, len(s.Prices))
		for j, p := range s.Prices {
			var opt service.ServicePriceOption
			if err := database.Where(service.ServicePriceOption{ServiceID: svc.ID, Name: p.Name}).
				Attrs(service.ServicePriceOption{PriceCents: p.Smn * 100, IsDefault: p.IsDefault}).
				FirstOrCreate(&opt).Error; err != nil {
				return nil, nil, err
			}
			priceIDs[i][j] = opt.ID
		}
	}
	return svcIDs, priceIDs, nil
}

func seedConnectionRequests(database *gorm.DB) error {
	str := func(s string) *string { return &s }
	num := func(n int) *int { return &n }
	reqs := []connectionrequest.ConnectionRequest{
		{BusinessName: "Автомойка «Сарвар»", ContactName: str("Сарвар Додоев"), ContactPhone: "+992901112233",
			Address: str("ул. Гафурова, 12, Худжанд"), BoxesCount: num(4), Note: str("Хотим подключиться с января"), Status: connectionrequest.StatusNew},
		// Minimal application, exactly what the landing form submits.
		{BusinessName: "Мойка у Кароматуллы", ContactPhone: "+992902223344", Status: connectionrequest.StatusNew},
		{BusinessName: "Аква-Экспресс", ContactName: str("Ильхом Бекназаров"), ContactPhone: "+992903334455",
			Address: str("пр. Сомони, 3, Куляб"), BoxesCount: num(1), Status: connectionrequest.StatusRejected},
	}
	for _, r := range reqs {
		var existing connectionrequest.ConnectionRequest
		err := database.Where(connectionrequest.ConnectionRequest{BusinessName: r.BusinessName, ContactPhone: r.ContactPhone}).
			Attrs(r).FirstOrCreate(&existing).Error
		if err != nil {
			return err
		}
	}
	return nil
}

func printSummary() {
	fmt.Println()
	fmt.Println("Demo data ready. Logins (password for all: " + demoPassword + "):")
	fmt.Println("  admin   (q-wash-admin)   " + adminLogin)
	for _, p := range demoPoints {
		fmt.Printf("  %-26s staff (q-wash-cabinet) %-12s worker (q-wash-worker) %s\n", p.Name, p.StaffUser, p.WorkUser)
	}
	fmt.Println("  display board: open q-wash-display and pick/enter the washing point")
	fmt.Println("Customer app (phone + OTP): live-demo client with no booking: " + demoCustomers[len(demoCustomers)-1].Phone)
	fmt.Println("  Other customers (+992900000001..013) hold the queue shown on the boards.")
	fmt.Println("  OTP: use your SMS stub's code, or set REVIEW_PHONE/REVIEW_OTP to the number above.")
}
