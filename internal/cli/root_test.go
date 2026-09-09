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

// root 現在可執行(無參數開互動式介面),連帶讓打錯的子命令回非零 exit code——在此之前
// cobra 對不可執行的 root 是印 help 然後成功返回,腳本裡的 capy pasue && echo ok 會印 ok。
func TestUnknownSubcommandFails(t *testing.T) {
	if _, err := runCLI(t, "pasue"); err == nil {
		t.Error("打錯的子命令要回非零 exit code,不能靜靜印 help 然後成功")
	}
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
