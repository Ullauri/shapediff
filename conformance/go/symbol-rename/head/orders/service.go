package orders

import (
	"context"
	"errors"
	"strings"
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
	if err := ValidateName(name); err != nil {
		return err
	}
	return s.store.Insert(ctx, name)
}

// ValidateName rejects empty or overlong names.
func ValidateName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return errors.New("empty name")
	}
	if len(trimmed) > 64 {
		return errors.New("name too long")
	}
	if strings.ContainsAny(trimmed, "\t\n") {
		return errors.New("control characters in name")
	}
	return nil
}
