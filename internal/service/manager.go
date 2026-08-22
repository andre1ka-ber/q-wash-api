package service

import (
	"context"

	"github.com/google/uuid"

	"q-wash-api/internal/apperror"
)

// Manager holds the business rules around services and their price
// options that go beyond plain CRUD: keeping "exactly one default price
// option" true, and guarding deletes that would leave a service without
// any price option or remove one still referenced by a booking.
type Manager struct {
	repo *Repository
}

func NewManager(repo *Repository) *Manager {
	return &Manager{repo: repo}
}

type PriceOptionInput struct {
	Name       string
	PriceCents int
	IsDefault  bool
}

func (m *Manager) CreateService(
	ctx context.Context,
	washingPointID uuid.UUID,
	name string,
	description *string,
	durationMinutes int,
	pictureURL *string,
	priceOptions []PriceOptionInput,
) (*Service, error) {
	opts := make([]ServicePriceOption, len(priceOptions))
	for i, po := range priceOptions {
		opts[i] = ServicePriceOption{Name: po.Name, PriceCents: po.PriceCents, IsDefault: po.IsDefault}
	}

	svc := &Service{
		WashingPointID:  washingPointID,
		Name:            name,
		Description:     description,
		DurationMinutes: durationMinutes,
		PictureURL:      pictureURL,
		IsActive:        true,
		PriceOptions:    opts,
	}
	if err := m.repo.Create(ctx, svc); err != nil {
		return nil, err
	}
	return svc, nil
}

// AddPriceOption appends a new price option to a service. If isDefault is
// true, any existing default for that service is unset first so exactly
// one default is maintained.
func (m *Manager) AddPriceOption(ctx context.Context, serviceID uuid.UUID, name string, priceCents int, isDefault bool) (*ServicePriceOption, error) {
	if _, err := m.repo.FindByID(ctx, serviceID); err != nil {
		return nil, err
	}
	if isDefault {
		if err := m.repo.UnsetDefaultPriceOptions(ctx, serviceID); err != nil {
			return nil, err
		}
	}
	opt := &ServicePriceOption{ServiceID: serviceID, Name: name, PriceCents: priceCents, IsDefault: isDefault}
	if err := m.repo.CreatePriceOption(ctx, opt); err != nil {
		return nil, err
	}
	return opt, nil
}

// UpdatePriceOption applies a partial update, preserving the "exactly one
// default per service" invariant. Explicitly unsetting the current sole
// default is rejected — the caller must mark another option default first.
func (m *Manager) UpdatePriceOption(ctx context.Context, id uuid.UUID, name *string, priceCents *int, isDefault *bool) (*ServicePriceOption, error) {
	opt, err := m.repo.FindPriceOptionByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if name != nil {
		opt.Name = *name
	}
	if priceCents != nil {
		opt.PriceCents = *priceCents
	}
	if isDefault != nil {
		if *isDefault {
			if err := m.repo.UnsetDefaultPriceOptions(ctx, opt.ServiceID); err != nil {
				return nil, err
			}
			opt.IsDefault = true
		} else if opt.IsDefault {
			return nil, apperror.BadRequest("cannot_unset_default", "cannot unset the only default price option; mark another one default instead")
		}
	}

	if err := m.repo.UpdatePriceOption(ctx, opt); err != nil {
		return nil, err
	}
	return opt, nil
}

// DeletePriceOption removes a price option, refusing to remove the last
// remaining one for a service or one still referenced by a queue booking.
// If the removed option was the default, another remaining option is
// promoted to default automatically so a service never ends up without one.
func (m *Manager) DeletePriceOption(ctx context.Context, id uuid.UUID) error {
	opt, err := m.repo.FindPriceOptionByID(ctx, id)
	if err != nil {
		return err
	}

	count, err := m.repo.CountPriceOptionsByService(ctx, opt.ServiceID)
	if err != nil {
		return err
	}
	if count <= 1 {
		return apperror.Conflict("last_price_option", "a service must have at least one price option")
	}

	inUse, err := m.repo.CountQueueUsingPriceOption(ctx, id)
	if err != nil {
		return err
	}
	if inUse > 0 {
		return apperror.Conflict("price_option_in_use", "price option is referenced by existing bookings and cannot be removed")
	}

	if err := m.repo.DeletePriceOption(ctx, id); err != nil {
		return err
	}

	if opt.IsDefault {
		next, err := m.repo.FirstPriceOptionExcept(ctx, opt.ServiceID, id)
		if err != nil {
			return err
		}
		if next != nil {
			next.IsDefault = true
			if err := m.repo.UpdatePriceOption(ctx, next); err != nil {
				return err
			}
		}
	}
	return nil
}
