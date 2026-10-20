//go:build !windows

package install

import "syscall"

func availableBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	if stat.Bavail <= 0 || stat.Bsize <= 0 {
		return 0, nil
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil
}
