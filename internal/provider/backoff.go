package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

const (
	MaxRetries = 3
	// MaxBackoff:Retry-After 由伺服器決定、無上限;超過就直接回錯讓使用者知道要等多久(PR #4 review)。
	MaxBackoff = 60 * time.Second
)

// Wait:可被 ctx 取消的等待(不可讓 Ctrl-C 失效)。測試替換點。
var Wait = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// BackoffStderr:退避提示的輸出(stderr 不污染 TSV)。測試替換點。
var BackoffStderr io.Writer = os.Stderr

type RateLimitError struct {
	Seconds int // 伺服器要求的秒數;達重試上限時為 0
	Message string
}

func (e *RateLimitError) Error() string { return e.Message }

func RetryAfterSeconds(resp *http.Response, fallback int) int {
	if s := resp.Header.Get("Retry-After"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			return n
		}
	}
	return fallback
}

// Backoff 處理一次 429:達上限 → RateLimitError(不等待);否則印提示並等待(可取消)。
func Backoff(ctx context.Context, resp *http.Response, attempt int) error {
	if attempt >= MaxRetries {
		return &RateLimitError{Message: "rate limited,重試已達上限"}
	}
	secs := RetryAfterSeconds(resp, 1)
	if secs > int(MaxBackoff/time.Second) {
		return &RateLimitError{Seconds: secs, Message: fmt.Sprintf("rate limited,伺服器要求等待 %d 秒(超過上限 %d 秒),請稍後再試", secs, int(MaxBackoff/time.Second))}
	}
	d := time.Duration(secs) * time.Second
	fmt.Fprintf(BackoffStderr, "rate limited,等待 %v 後重試…\n", d)
	return Wait(ctx, d)
}
