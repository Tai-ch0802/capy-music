package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

func stubWait(t *testing.T) *[]time.Duration {
	t.Helper()
	var got []time.Duration
	orig := Wait
	Wait = func(_ context.Context, d time.Duration) error { got = append(got, d); return nil }
	t.Cleanup(func() { Wait = orig })
	return &got
}

func resp429(retryAfter string) *http.Response {
	h := http.Header{}
	if retryAfter != "" {
		h.Set("Retry-After", retryAfter)
	}
	return &http.Response{StatusCode: 429, Header: h}
}

func TestBackoffWaitsAndLogs(t *testing.T) {
	waits := stubWait(t)
	buf := &bytes.Buffer{}
	orig := BackoffStderr
	BackoffStderr = buf
	t.Cleanup(func() { BackoffStderr = orig })

	if err := Backoff(context.Background(), resp429("7"), 0); err != nil {
		t.Fatal(err)
	}
	if len(*waits) != 1 || (*waits)[0] != 7*time.Second {
		t.Errorf("應等 7s,實際 %v", *waits)
	}
	if !bytes.Contains(buf.Bytes(), []byte("rate limited")) {
		t.Errorf("應印提示到 stderr:%q", buf.String())
	}
}

func TestBackoffRefusesExcessiveRetryAfter(t *testing.T) {
	waits := stubWait(t)
	err := Backoff(context.Background(), resp429("3600"), 0)
	var rl *RateLimitError
	if !errors.As(err, &rl) || rl.Seconds != 3600 {
		t.Fatalf("超過上限應回 RateLimitError 含秒數,得到 %v", err)
	}
	if len(*waits) != 0 {
		t.Error("超過上限不應等待")
	}
}

func TestBackoffGivesUpAtMaxRetries(t *testing.T) {
	stubWait(t)
	var rl *RateLimitError
	if err := Backoff(context.Background(), resp429("1"), MaxRetries); !errors.As(err, &rl) {
		t.Fatalf("達重試上限應回 RateLimitError,得到 %v", err)
	}
}

func TestBackoffCancellable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := Backoff(ctx, resp429("30"), 0) // 真實 Wait
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatalf("ctx 取消應立即回 DeadlineExceeded:%v(耗時 %v)", err, time.Since(start))
	}
}

func TestRetryAfterSecondsFallback(t *testing.T) {
	if got := RetryAfterSeconds(resp429(""), 1); got != 1 {
		t.Errorf("無標頭應用 fallback:%d", got)
	}
	if got := RetryAfterSeconds(resp429("abc"), 1); got != 1 {
		t.Errorf("非數字應用 fallback:%d", got)
	}
}

func TestBackoffExponentialFallback(t *testing.T) {
	var got []time.Duration
	orig, origErr := Wait, BackoffStderr
	Wait = func(_ context.Context, d time.Duration) error { got = append(got, d); return nil }
	BackoffStderr = io.Discard
	t.Cleanup(func() { Wait, BackoffStderr = orig, origErr })
	for attempt := 0; attempt < MaxRetries; attempt++ {
		if err := Backoff(context.Background(), resp429(""), attempt); err != nil {
			t.Fatal(err)
		}
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("無 Retry-After 應指數退避 %v,得 %v", want, got)
	}
}

// TestBackoffWithoutWaitReturnsSeconds:輪詢(web 的 /api/now)不睡在鎖裡——直接回 RateLimitError 帶 Retry-After 的秒數,
// 冷卻交給呼叫端。就算秒數在上限內也不等、不印等待提示。
func TestBackoffWithoutWaitReturnsSeconds(t *testing.T) {
	waits := stubWait(t)
	buf := &bytes.Buffer{}
	orig := BackoffStderr
	BackoffStderr = buf
	t.Cleanup(func() { BackoffStderr = orig })

	err := Backoff(WithoutWait(context.Background()), resp429("7"), 0)
	var rl *RateLimitError
	if !errors.As(err, &rl) || rl.Seconds != 7 {
		t.Fatalf("WithoutWait 應立即回 RateLimitError{Seconds: 7},得到 %v", err)
	}
	if len(*waits) != 0 || buf.Len() != 0 {
		t.Errorf("WithoutWait 不可以等,也不印等待提示:waits=%v stderr=%q", *waits, buf.String())
	}
}
