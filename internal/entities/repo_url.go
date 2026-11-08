package entities

import (
	"errors"
	"net/url"
	"strings"
)

// MaxRepoURLLength bounds a repository URL: it is a piece of project metadata
// that rides the agent brief, not a field a person writes prose into.
const MaxRepoURLLength = 2048

// NormalizeRepoURL trims and validates an optional project repository URL.
//
// The empty string is valid and means "no repository". Only http, https and ssh
// are accepted: those are what an agent can hand to git without the server
// inventing a transport. Embedded credentials are refused rather than stripped,
// because a URL carrying a password is a secret that would otherwise reach the
// change log and the agent brief. A username alone is allowed: `git@host` is the
// conventional SSH form, not a credential.
func NormalizeRepoURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if len(trimmed) > MaxRepoURLLength {
		return "", errors.New("repository URL is too long")
	}
	if strings.ContainsAny(trimmed, "\r\n\t") {
		return "", errors.New("repository URL contains control characters")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", errors.New("repository URL is not a valid URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "ssh":
	default:
		return "", errors.New("repository URL must use http, https or ssh")
	}
	if parsed.Host == "" {
		return "", errors.New("repository URL needs a host")
	}
	if parsed.User != nil {
		if _, hasPassword := parsed.User.Password(); hasPassword {
			return "", errors.New("repository URL must not embed a password")
		}
	}
	return trimmed, nil
}
