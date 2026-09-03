//go:build windows

package auth

import (
	"os"

	"golang.org/x/sys/windows"
)

// LockFileEx 是 per-handle 的 byte-range 鎖:鎖第 0 個 byte(空檔也能鎖到 EOF 之後的範圍)。
// Unlock 必須用完全相同的範圍與 Overlapped 偏移,不然會編譯過但靜默解不開。
func flock(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &windows.Overlapped{})
}

func funlock(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &windows.Overlapped{})
}
