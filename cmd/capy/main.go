package main

import (
	"fmt"
	"os"

	"github.com/Tai-ch0802/capy-music/internal/cli"
)

// exit code 對照表在 cli.ExitCode:0 成功、1 錯誤、2 需要人介入(候選 / 變更集已印在 stdout)、3 安全閥擋下。
func main() {
	code, msg := cli.ExitCode(cli.Execute())
	if msg != "" {
		fmt.Fprintln(os.Stderr, msg)
	}
	os.Exit(code)
}
