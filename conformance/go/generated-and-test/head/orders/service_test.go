package orders

import "testing"

func TestValidate(t *testing.T) {
	if err := Validate(""); err == nil {
		t.Fatal("empty name accepted")
	}
}

func TestValidateLong(t *testing.T) {
	if err := Validate(string(make([]byte, 65))); err == nil {
		t.Fatal("long name accepted")
	}
}
