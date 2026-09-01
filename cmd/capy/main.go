package main

import (
	"os"

	"github.com/Tai-ch0802/capy-music/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
