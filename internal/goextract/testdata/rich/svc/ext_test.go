package svc_test

import (
	"testing"

	"example.com/rich/svc"
)

func TestExternal(t *testing.T) {
	if svc.NewServer() == nil {
		t.Fatal("nil")
	}
}
