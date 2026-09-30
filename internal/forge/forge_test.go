package forge

import "testing"

func TestLinks(t *testing.T) {
	cases := []struct {
		f          Forge
		blob, tree string
		change     string
	}{
		{"", "https://h/g/s/-/blob/abc/a/b.go#L7", "https://h/g/s/-/tree/abc/a", "MR"},
		{GitLab, "https://h/g/s/-/blob/abc/a/b.go#L7", "https://h/g/s/-/tree/abc/a", "MR"},
		{GitHub, "https://h/g/s/blob/abc/a/b.go#L7", "https://h/g/s/tree/abc/a", "PR"},
	}
	for _, c := range cases {
		if got := c.f.Blob("https://h/g/s/", "abc", "a/b.go", 7); got != c.blob {
			t.Errorf("%q Blob = %s, want %s", c.f, got, c.blob)
		}
		if got := c.f.Tree("https://h/g/s", "abc", "a"); got != c.tree {
			t.Errorf("%q Tree = %s, want %s", c.f, got, c.tree)
		}
		if got := c.f.Change(); got != c.change {
			t.Errorf("%q Change = %s, want %s", c.f, got, c.change)
		}
	}
}

func TestFromURL(t *testing.T) {
	for u, want := range map[string]Forge{
		"https://github.com/Ullauri/devlogplus": GitHub,
		"https://GitHub.com/o/r/":               GitHub,
		"https://gitlab.example.com/g/s":        GitLab,
		"https://github.example.com/o/r":        GitLab,
		"":                                      GitLab,
		"::not a url":                           GitLab,
	} {
		if got := FromURL(u); got != want {
			t.Errorf("FromURL(%q) = %q, want %q", u, got, want)
		}
	}
}

func TestParse(t *testing.T) {
	for in, want := range map[string]Forge{"": GitLab, "gitlab": GitLab, "GitHub": GitHub, " github ": GitHub} {
		if got, err := Parse(in); err != nil || got != want {
			t.Errorf("Parse(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := Parse("bitbucket"); err == nil {
		t.Error("Parse(bitbucket) should fail")
	}
}
