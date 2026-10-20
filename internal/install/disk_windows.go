//go:build windows

package install

// availableBytes is deliberately unknown on Windows: the standard library has
// no disk-free call without another dependency, and an unknown value must not
// block an install Docker Desktop can complete.
func availableBytes(path string) (uint64, error) {
	return 0, nil
}
