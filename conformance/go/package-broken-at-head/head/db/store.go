package db

import (
	"context"
	"database/sql"
)

// SQLStore is the orders.Store over Postgres.
type SQLStore struct {
	q *Queries
}

// NewSQLStore wires a store to a connection.
func NewSQLStore(conn *sql.DB) *SQLStore {
	return &SQLStore{q: New(conn)}
}

// Insert stores an order.
func (s *SQLStore) Insert(ctx context.Context, name string) error {
	return s.q.InsertOrder(ctx, undefinedName)
}
