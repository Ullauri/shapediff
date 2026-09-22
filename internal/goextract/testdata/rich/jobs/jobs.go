// Package jobs is a worker registry matched through repo config.
package jobs

import "context"

var registry = map[string]func(context.Context) error{}

// Register records a handler under name.
func Register(name string, h func(context.Context) error) { registry[name] = h }

// ProcessRefund is a worker handler.
func ProcessRefund(ctx context.Context) error { return nil }
