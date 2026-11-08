package entities

import (
	"strings"
	"testing"
)

func TestNormalizeRepoURL(t *testing.T) {
	valid := map[string]string{
		"empty is no repository":       "",
		"https":                        "https://github.com/acme/demo.git",
		"http":                         "http://git.internal/acme/demo",
		"ssh":                          "ssh://git@example.com/acme/demo.git",
		"trimmed":                      "  https://example.com/acme/demo  ",
		"ssh user is not a credential": "ssh://git@example.com/acme/demo.git",
	}
	for name, raw := range valid {
		t.Run(name, func(t *testing.T) {
			got, err := NormalizeRepoURL(raw)
			if err != nil {
				t.Fatalf("NormalizeRepoURL(%q) = %v, want accepted", raw, err)
			}
			if got != strings.TrimSpace(raw) {
				t.Errorf("got %q, want %q", got, strings.TrimSpace(raw))
			}
		})
	}

	invalid := map[string]string{
		"ftp":                 "ftp://example.com/acme/demo",
		"local path":          "/srv/git/demo.git",
		"scp shorthand":       "git@github.com:acme/demo.git",
		"no host":             "https:///acme/demo",
		"embedded password":   "https://user:secret@example.com/acme/demo.git",
		"newline":             "https://example.com/acme\n/demo",
		"over the length cap": "https://example.com/" + strings.Repeat("a", MaxRepoURLLength),
	}
	for name, raw := range invalid {
		t.Run(name, func(t *testing.T) {
			if got, err := NormalizeRepoURL(raw); err == nil {
				t.Errorf("NormalizeRepoURL(%q) = %q, want refused", raw, got)
			}
		})
	}
}
