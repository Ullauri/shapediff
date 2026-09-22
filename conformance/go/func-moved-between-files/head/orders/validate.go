package orders

import (
	"errors"
	"strings"
)

// Validate rejects empty or overlong names.
func Validate(name string) error {
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
