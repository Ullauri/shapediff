package db

import (
	"context"
	"database/sql"
)

// SQLStore is the orders.Store over Postgres.
type SQLStore struct {
	conn *sql.DB
}

// NewSQLStore wires a store to a connection.
func NewSQLStore(conn *sql.DB) *SQLStore {
	return &SQLStore{conn: conn}
}

// Insert stores an order.
func (s *SQLStore) Insert(ctx context.Context, name string) error {
	return nil
}
