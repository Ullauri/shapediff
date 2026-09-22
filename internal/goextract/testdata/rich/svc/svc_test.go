package svc

import "testing"

func TestCreate(t *testing.T) {
	s := NewServer()
	if s == nil {
		t.Fatal("nil")
	}
}
