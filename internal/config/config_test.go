package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultsAndHash(t *testing.T) {
	dir := t.TempDir()
	c, err := LoadRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Budget.Nodes != 24 || c.NarrowTargets != 2 || c.Narration != "template" {
		t.Fatalf("defaults: %+v", c)
	}
	h1 := c.Hash()
	os.WriteFile(filepath.Join(dir, FileName), []byte("budget:\n  nodes: 24\n"), 0o644)
	c2, _ := LoadRepo(dir)
	if c2.Hash() != h1 {
		t.Error("explicit default should hash the same as the implicit one")
	}
	os.WriteFile(filepath.Join(dir, FileName), []byte("budget:\n  nodes: 30\nnarration: off\n"), 0o644)
	c3, err := LoadRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c3.Hash() == h1 || c3.Budget.Edges != 45 || c3.Narration != "off" {
		t.Errorf("changed config: %+v", c3)
	}
	os.WriteFile(filepath.Join(dir, FileName), []byte("narration: prose\n"), 0o644)
	if _, err := LoadRepo(dir); err == nil {
		t.Error("bad narration value accepted")
	}
}
