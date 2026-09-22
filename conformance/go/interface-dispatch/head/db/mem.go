package db

import "context"

// MemStore keeps orders in memory.
type MemStore struct {
	names []string
}

// Insert appends the order.
func (m *MemStore) Insert(ctx context.Context, name string) error {
	m.names = append(m.names, name)
	return nil
}
