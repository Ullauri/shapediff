package orders

import "testing"

func TestValidate(t *testing.T) {
	if err := Validate(""); err == nil {
		t.Fatal("empty name accepted")
	}
}
