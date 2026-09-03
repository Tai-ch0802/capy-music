//go:build !darwin

package apple

import (
	"errors"
	"testing"
)

func TestAutoWebTokensUnsupported(t *testing.T) {
	wt, err := AutoWebTokens()
	if !errors.Is(err, ErrAutoUnsupported) {
		t.Fatalf("err = %v, want ErrAutoUnsupported", err)
	}
	if wt != (WebTokens{}) {
		t.Errorf("不支援平台不該回傳資料:%+v", wt)
	}
}
