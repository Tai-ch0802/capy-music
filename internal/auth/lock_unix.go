//go:build !windows

package auth

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryFlock / funlock:BSD flock,per-open-file-description——同一 process 開兩個 fd 也會互斥,測試才測得到。
// 不用 fcntl(per-process,同 process 內永不互斥)。EINTR 重試:Go 的搶佔訊號可能打斷 flock。
//
// 一律 LOCK_NB(非阻塞):阻塞版不吃 context,Ctrl-C 只取消 ctx、不會終止 process,卡在 flock 系統呼叫的
// goroutine 永遠看不到取消,只剩 SIGKILL 殺得掉(spec:可被 cron 終止是核心價值)。等待改由 lockFile
// 在 Go 這層輪詢,ctx 取消就放棄。回傳 (true, nil) 表示取得;(false, nil) 表示別人持有中。
func tryFlock(f *os.File) (bool, error) {
	for {
		switch err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); {
		case err == nil:
			return true, nil
		case errors.Is(err, unix.EWOULDBLOCK):
			return false, nil // EWOULDBLOCK == EAGAIN:鎖被別的 capy 持有
		case errors.Is(err, unix.EINTR):
			continue
		default:
			return false, err
		}
	}
}

func funlock(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
