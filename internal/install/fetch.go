// Package install owns the tt CLI's install state, its pure plan, and the
// adapter that executes a plan. The plan reads only its arguments; the
// default-path helpers are the only environment-aware code here.
package install

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MaxFetchBytes is the largest file FetchBinary will download. It is a defence
// against a misconfigured release host serving a multi-gigabyte artifact where
// the operator expected a static binary, and against a slow connection that
// would otherwise run away. 64 MiB covers a static Go binary with headroom.
const MaxFetchBytes = 64 * 1024 * 1024

// FetchBinary downloads url to dest over HTTPS, then makes it executable. It
// is the only place this package touches the network, and it is the seam the
// tests exercise: every behaviour below is provable with an httptest server.
//
// The URL must be https, the host must be explicit, and the response must not
// exceed MaxFetchBytes. The download is written to a `.part` file beside dest
// and renamed into place, so a partial download is never observed as the real
// binary and a rename across filesystems cannot strand it.
func FetchBinary(ctx context.Context, client *http.Client, rawURL, dest string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse %q: %w", rawURL, err)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("refuse %q: only https is accepted, so a fetched binary is not served over a sniffable connection", rawURL)
	}
	if parsed.Host == "" {
		return fmt.Errorf("refuse %q: no host", rawURL)
	}

	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	part := dest + ".part"
	out, err := os.Create(part)
	if err != nil {
		return fmt.Errorf("create %s: %w", part, err)
	}
	defer func() {
		_ = out.Close()
		_ = os.Remove(part)
	}()

	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download %s: %w", rawURL, err)
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// Drain the body so the server does not hold the connection open.
		io.Copy(io.Discard, response.Body)
		return fmt.Errorf("download %s: server answered %s", rawURL, response.Status)
	}

	written, err := io.Copy(out, io.LimitReader(response.Body, MaxFetchBytes+1))
	if err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	if written > MaxFetchBytes {
		return fmt.Errorf("refuse %s: %d bytes exceeds the %d-byte cap", rawURL, written, MaxFetchBytes)
	}

	if err := out.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", dest, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %s: %w", dest, err)
	}
	if err := os.Rename(part, dest); err != nil {
		return fmt.Errorf("rename %s to %s: %w", part, dest, err)
	}
	if err := os.Chmod(dest, 0o755); err != nil {
		return fmt.Errorf("chmod %s: %w", dest, err)
	}
	return nil
}

// FetchBinaryURL is the production seam: it uses a real client and reports
// the URL it fetched, so the CLI can name the binary in its output.
var FetchBinaryURL = func(ctx context.Context, url, dest string) error {
	return FetchBinary(ctx, nil, url, dest)
}

// IsHTTPRefused reports whether err came from FetchBinary refusing the scheme
// or host of a URL. It lets the CLI print a single, actionable line instead of
// wrapping the parse error in the download error.
func IsHTTPRefused(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "only https is accepted") ||
		strings.Contains(err.Error(), "no host")
}

// BinaryExists reports whether dest is an executable file.
func BinaryExists(dest string) bool {
	info, err := os.Stat(dest)
	if err != nil {
		return false
	}
	return !info.IsDir() && info.Mode()&0o111 != 0
}
