//go:build windows

package auth

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// LockFileEx 是 per-handle 的 byte-range 鎖:鎖第 0 個 byte(空檔也能鎖到 EOF 之後的範圍)。
// Unlock 必須用完全相同的範圍與 Overlapped 偏移(reserved=0, bytesLow=1, bytesHigh=0, 零值 Overlapped),
// 不然會編譯過但靜默解不開——只有 flags 這個參數兩邊不同(Unlock 沒有 flags)。
//
// 一律 LOCKFILE_FAIL_IMMEDIATELY(非阻塞),理由同 lock_unix.go:阻塞版不吃 context,Ctrl-C 殺不掉。
// 等待由 lockFile 在 Go 這層輪詢。鎖被佔用時回 ERROR_LOCK_VIOLATION;overlapped handle 上也可能回
// ERROR_IO_PENDING,兩者都當「別人持有中」(本檔無法在本機執行,寧可寬鬆)。
func tryFlock(f *os.File) (bool, error) {
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION), errors.Is(err, windows.ERROR_IO_PENDING):
		return false, nil
	default:
		return false, err
	}
}

func funlock(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &windows.Overlapped{})
}
