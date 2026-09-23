package cli

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/huh/v2"
	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

// newForm:全 CLI 的 huh 表單都從這裡建,共用一份鍵位 —— Esc 也能取消。huh 預設只有 Ctrl-C 會中止,
// Esc 是過濾模式裡「離開 / 清掉過濾字」的鍵,所以挑選器開著按 Esc 什麼都不會發生(使用者回報)。
// 表單層先比 Quit 再把按鍵交給欄位,綁上之後過濾模式裡 Esc 也是取消;Ctrl-C 照舊。
// 這等於拿掉 select 過濾模式的兩個 Esc 動作(留著過濾字離開輸入、清掉過濾字):過濾中只剩 Enter 選、退格清字。
// 它們的鍵位還在 help 行裡(select 的 KeyBinds 固定列它們,setFiltering 每次重設 Enabled,壓不掉),
// 所以只改 help 文字,別讓它宣傳做不到的事(PR #50 review)。MultiSelect 沒用到,不補。
// 直接叫 huh.NewForm 會漏掉這份鍵位與配色,新表單一律走這裡。
// **web 模式(capy --web,P7 決策 40)**:installWebSeams 把 stdinIsTTY 換成 false,但 huh 仍會抓 /dev/tty——伺服器行程有
// 終端機就接管、沒有就「bubbletea: error opening TTY」。所以任何新的表單一律包成 package var 並進 installWebSeams
// (CLAUDE.md 硬約束),不得在命令裡直接 newForm(...).Run();既有的都在 isInteractive / bothTTY / stdinIsTTY 閘後面。
// 配色是 ui.HuhStyles(跟互動式介面同一套):huh 預設把標題染成靛藍,深色終端機上很難讀(使用者回報)。
func newForm(groups ...*huh.Group) *huh.Form {
	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("ctrl+c", "esc"))
	km.Select.SetFilter = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel"), key.WithDisabled())
	km.Select.ClearFilter = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel"), key.WithDisabled())
	return huh.NewForm(groups...).WithKeyMap(km).WithTheme(huh.ThemeFunc(ui.HuhStyles))
}

// pickOne:全 CLI 共用的挑選器,回選中的索引。標題的括號提示由這裡補,呼叫端只給主詞。
// 八筆以下不開過濾:多打一次 / 才看得到全部,對短清單只是雜訊。測試替換點。
var pickOne = func(title string, labels []string) (int, error) {
	opts := make([]huh.Option[int], len(labels))
	for i, l := range labels {
		opts[i] = huh.NewOption(l, i)
	}
	filter := len(labels) > 8
	hint := "(Esc 取消)"
	if filter {
		hint = "(/ 過濾,Esc 取消)"
	}
	idx := 0
	sel := huh.NewSelect[int]().Title(title + hint).Options(opts...).Filtering(filter).Height(12).Value(&idx)
	if err := newForm(huh.NewGroup(sel)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return 0, errCancelled
		}
		return 0, err
	}
	return idx, nil
}

// promptNewName:pl link 選「建立新的清單」後問名字。測試替換點。
var promptNewName = func(title string) (string, error) {
	name := ""
	if err := newForm(huh.NewGroup(huh.NewInput().Title(title).Value(&name))).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return "", errCancelled
		}
		return "", err
	}
	if name = strings.TrimSpace(name); name == "" {
		return "", errCancelled
	}
	return name, nil
}

var errCancelled = i18n.Errorf("pick.err.cancelled")

// argsOrPicker:0 個參數只在終端機裡放行(RunE 會開挑選器補),其餘一律走 cobra 原本的檢查 ——
// 非 TTY(管線 / cron)看到的錯誤訊息一個字都不能變,可腳本化是硬約束。
func argsOrPicker(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 && isInteractive(cmd) {
			return nil
		}
		return cobra.ExactArgs(n)(cmd, args)
	}
}

// needTarget:pl pull / push / sync 的參數檢查。清單名與 --all 互斥;兩個都沒給時,
// 只有終端機能靠挑選器補,非 TTY 維持原本的錯誤。
func needTarget(cmd *cobra.Command, args []string, all bool, verb string) error {
	bothGiven := all && len(args) == 1
	neitherGiven := !all && len(args) == 0
	if bothGiven || (neitherGiven && !isInteractive(cmd)) { // 都沒給時只有終端機能靠挑選器補
		return fmt.Errorf("指定一個清單(名稱或 pid),或用 --all %s全部已連結的清單", verb)
	}
	return nil
}

// pickPlatformPlaylist:從平台的清單列表挑一個,回它的 provider 端 ID。newLabel 非空時列在最後一列,選它回 ""
// (pl link 的「在平台上建一個新的空清單」;平台上一個清單都沒有時也還能選它)。
func pickPlatformPlaylist(prov string, refs []provider.PlaylistRef, newLabel string) (string, error) {
	if len(refs) == 0 && newLabel == "" {
		return "", fmt.Errorf("%s 上沒有任何清單", prov)
	}
	labels := make([]string, len(refs), len(refs)+1)
	for i, r := range refs {
		labels[i] = r.Name
		if r.Total >= 0 {
			labels[i] += fmt.Sprintf(" — %d 首", r.Total)
		}
	}
	if newLabel != "" {
		labels = append(labels, newLabel)
	}
	i, err := pickOne(fmt.Sprintf("選一個 %s 上的清單", prov), labels)
	if err != nil {
		return "", err
	}
	if i == len(refs) {
		return "", nil
	}
	return refs[i].ID, nil
}

// pickProvider:列全部平台,不先探測誰有登入 —— 探測要建 provider 又慢,
// 而選到沒登入的平台時,後面的錯誤訊息本來就會指路 auth login。
func pickProvider(title string) (string, error) {
	i, err := pickOne(title, providerIDs)
	if err != nil {
		return "", err
	}
	return providerIDs[i], nil
}

// linkedPlaylists:有連結的 canonical 清單,依名稱排序;prov 非空時只留連了那個平台的。
func linkedPlaylists(s *canonState, prov string) []*canon.Playlist {
	var out []*canon.Playlist
	for _, pid := range slices.Sorted(maps.Keys(s.playlists)) {
		pl := s.playlists[pid]
		if (prov == "" && len(pl.Links) > 0) || (prov != "" && pl.Links[prov] != "") {
			out = append(out, pl)
		}
	}
	slices.SortStableFunc(out, func(a, b *canon.Playlist) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// linkSummary:「spotify:p1、apple:p.x」,挑選器用來讓同名清單分得出來。
func linkSummary(pl *canon.Playlist) string {
	parts := make([]string, 0, len(pl.Links))
	for _, prov := range slices.Sorted(maps.Keys(pl.Links)) {
		parts = append(parts, prov+":"+pl.Links[prov])
	}
	return strings.Join(parts, "、")
}

// pickLinkedPlaylist:pl pull / push / sync / unlink 不帶參數時挑一個已連結的清單。
// 回 pid 不回名字:名字會撞到同名,pid 讓 find 直接命中。
func pickLinkedPlaylist(s *canonState, prov, title string) (string, error) {
	pls := linkedPlaylists(s, prov)
	if len(pls) == 0 {
		if prov != "" {
			return "", fmt.Errorf("沒有連結 %s 的清單 — 先 capy pl link <name> %s:<清單>", prov, prov)
		}
		return "", errors.New("沒有已連結的清單 — 先 capy pl link <name> <provider>:<清單>")
	}
	labels := make([]string, len(pls))
	for i, pl := range pls {
		labels[i] = pl.Name + " — " + linkSummary(pl)
	}
	i, err := pickOne(title, labels)
	if err != nil {
		return "", err
	}
	return pls[i].PID, nil
}

// pickLinkTarget:pl link 的第三段 —— 挑要連到哪個 canonical 清單,或建一個新的。
// 全部清單都列(還沒連過任何平台的也是合法目標)。選既有回 pid(不回名字,名字會撞同名),
// 選「建立新的清單」回打進去的名字 —— 呼叫端拿它去 s.find,所以這裡先擋掉會被 find 命中的
// 兩種輸入(既有的名字、既有的 pid):不擋的話,使用者明明按了「建立新的」,卻被靜靜 link 到舊的那個。
// 已經佔用這個平台清單的那一列會標出來:選別列會被 RunE 以「只能連一個」擋下。
// id 是空的 = 要在平台上建新清單,還沒有誰佔用它(沒連這個平台的清單,Links[prov] 也是空的,別被當成「已連結到這個清單」)。
func pickLinkTarget(s *canonState, prov, id string) (string, error) {
	pls := make([]*canon.Playlist, 0, len(s.playlists))
	for _, pid := range slices.Sorted(maps.Keys(s.playlists)) {
		pls = append(pls, s.playlists[pid])
	}
	slices.SortStableFunc(pls, func(a, b *canon.Playlist) int { return strings.Compare(a.Name, b.Name) })
	labels := make([]string, 0, len(pls)+1)
	for _, pl := range pls {
		switch cur := pl.Links[prov]; {
		case cur != "" && cur == id:
			labels = append(labels, pl.Name+" — 已連結到這個清單")
		case cur != "":
			labels = append(labels, fmt.Sprintf("%s — 已連 %s:%s,要先 unlink", pl.Name, prov, cur))
		case len(pl.Links) > 0:
			labels = append(labels, pl.Name+" — "+linkSummary(pl))
		default:
			labels = append(labels, pl.Name)
		}
	}
	labels = append(labels, "+ 建立新的清單")
	i, err := pickOne("要連結哪個清單?", labels)
	if err != nil {
		return "", err
	}
	if i < len(pls) {
		return pls[i].PID, nil
	}
	name, err := promptNewName("新清單的名稱")
	if err != nil {
		return "", err
	}
	for _, pl := range pls { // find 認名字也認 pid,兩種都要擋;同名兩個之後也只能用 pid 指名了
		if strings.EqualFold(pl.Name, name) || pl.PID == name {
			return "", fmt.Errorf("已經有這個清單了:%s(%s)—— 回去選它,或換一個名字", pl.Name, pl.PID)
		}
	}
	return name, nil
}
