package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// 命令名稱不得重複:cobra 的 AddCommand 只是 append,重複註冊不會 panic、Find 只會命中第一個,
// 所以測試全綠但 capy --help 與 shell 補全都會印兩行(PR #40 review 實際踩到)。遞迴檢查每一層。
func TestNoDuplicateCommandNames(t *testing.T) {
	var walk func(path string, c *cobra.Command)
	walk = func(path string, c *cobra.Command) {
		seen := map[string]bool{}
		for _, sub := range c.Commands() {
			if seen[sub.Name()] {
				t.Errorf("%s 底下有重複的子命令 %q", path, sub.Name())
			}
			seen[sub.Name()] = true
			walk(path+" "+sub.Name(), sub)
		}
	}
	walk("capy", newRootCmd())
}

func TestRootVersion(t *testing.T) {
	cmd := newRootCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"--version"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(buf.String(), "dev") {
		t.Errorf("版本輸出應含 dev,得到 %q", buf.String())
	}
}
