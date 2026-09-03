//go:build !darwin

package cli

import (
	"context"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

func init() {
	checkOSA = func(context.Context) (string, error) { return "", provider.ErrNotSupported }
}
