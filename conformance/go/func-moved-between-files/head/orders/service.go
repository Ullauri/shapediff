package orders

import (
	"context"
)

// Store persists orders.
type Store interface {
	Insert(ctx context.Context, name string) error
}

// Service creates orders.
type Service struct {
	store Store
}

// New returns a Service over store.
func New(store Store) *Service {
	return &Service{store: store}
}

// Create validates and stores an order.
func (s *Service) Create(ctx context.Context, name string) error {
	if err := Validate(name); err != nil {
		return err
	}
	return s.store.Insert(ctx, name)
}

