//go:build !windows

package auth

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// flock / funlock:BSD flock,per-open-file-description——同一 process 開兩個 fd 也會互斥,測試才測得到。
// 不用 fcntl(per-process,同 process 內永不互斥)。EINTR 重試:Go 的搶佔訊號可能打斷阻塞中的 flock。
func flock(f *os.File) error {
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

func funlock(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
