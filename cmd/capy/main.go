package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/Tai-ch0802/capy-music/internal/cli"
)

// exit code:0 成功、1 錯誤、2 需要人介入(候選已印在 stdout,不再印訊息)。
func main() {
	err := cli.Execute()
	if err == nil {
		return
	}
	var amb *cli.AmbiguousError
	if errors.As(err, &amb) {
		fmt.Fprintln(os.Stderr, err) // 候選在 stdout(TSV 不受影響),原因給人看
		os.Exit(2)
	}
	fmt.Fprintln(os.Stderr, "Error:", err)
	os.Exit(1)
}
