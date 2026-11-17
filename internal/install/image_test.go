package install

import "testing"

// The repository is an external contract: the release workflow publishes this
// path and every installed tt pulls it. Pinning the literal makes a rename a
// deliberate edit on both sides instead of a silent split.
func TestImageRefPinsThePublishedRepository(t *testing.T) {
	const want = "ghcr.io/amirashouri/tasktracker-server:v1.2.3"
	if got := ImageRef("v1.2.3"); got != want {
		t.Fatalf("ImageRef = %q, want %q; update the release workflow with it", got, want)
	}
}
