package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/canon"
)

// pickLog:挑選器被叫過幾次、每次的標題與選項。
type pickLog struct {
	titles []string
	labels [][]string
}

func (l *pickLog) nth(i int) []string {
	if i >= len(l.labels) {
		return nil
	}
	return l.labels[i]
}

// stubPickers:假裝在終端機裡,並把挑選器換成 choose(依序決定每一段選哪個索引)。
// choose 給 -1 表示使用者取消。
func stubPickers(t *testing.T, choose ...int) *pickLog {
	t.Helper()
	origTTY, origPick := isInteractive, pickOne
	log := &pickLog{}
	isInteractive = func(*cobra.Command) bool { return true }
	pickOne = func(title string, labels []string) (int, error) {
		log.titles = append(log.titles, title)
		log.labels = append(log.labels, labels)
		n := len(log.titles) - 1
		if n >= len(choose) {
			t.Fatalf("第 %d 段挑選器沒有預備答案:%q %v", n+1, title, labels)
		}
		if choose[n] < 0 {
			return 0, errCancelled
		}
		return choose[n], nil
	}
	t.Cleanup(func() { isInteractive, pickOne = origTTY, origPick })
	return log
}

// stubNewName:pl link 第三段選「建立新的清單」後打的名字。
func stubNewName(t *testing.T, name string) {
	t.Helper()
	orig := promptNewName
	promptNewName = func(string) (string, error) {
		if name == "" {
			return "", errCancelled
		}
		return name, nil
	}
	t.Cleanup(func() { promptNewName = orig })
}

// 非 TTY 下的錯誤訊息一個字都不能變 —— 可腳本化是硬約束,挑選器不得被觸發。
func TestPickerNonTTYKeepsExactErrors(t *testing.T) {
	pullWorld(t)
	orig := pickOne
	pickOne = func(string, []string) (int, error) {
		t.Fatal("非 TTY 不可開挑選器")
		return 0, nil
	}
	t.Cleanup(func() { pickOne = orig })
	for _, c := range []struct{ args, want string }{
		{"pl show", "accepts 1 arg(s), received 0"},
		{"pl link", "accepts 2 arg(s), received 0"},
		{"pl unlink", "accepts 2 arg(s), received 0"},
		{"pl pull", "指定一個清單(名稱或 pid),或用 --all 拉全部已連結的清單"},
		{"pl push", "指定一個清單(名稱或 pid),或用 --all 推全部已連結的清單"},
		{"pl sync", "指定一個清單(名稱或 pid),或用 --all 同步全部已連結的清單"},
	} {
		_, _, err := runPull(t, strings.Fields(c.args)...)
		if err == nil || err.Error() != c.want {
			t.Errorf("capy %s 非 TTY:%v,want %q", c.args, err, c.want)
		}
	}
	// 參數數量不對時也維持 cobra 的原訊息(挑選器只補「一個都沒給」)。
	if _, _, err := runPull(t, "pl", "link", "只有一個"); err == nil || err.Error() != "accepts 2 arg(s), received 1" {
		t.Errorf("pl link 一個參數:%v", err)
	}
}

func TestPlShowPicker(t *testing.T) {
	fs, _, _ := pullWorld(t)
	fs.set("p1", "通勤", "t1")
	fs.set("p2", "冬日暖調", "t2", "t3")
	log := stubPickers(t, 1)
	out, _ := mustPull(t, "pl", "show")
	if got := log.nth(0); len(got) != 2 || !strings.HasPrefix(got[0], "通勤") || !strings.HasPrefix(got[1], "冬日暖調") {
		t.Fatalf("挑選器要列平台的清單:%v", got)
	}
	if !strings.Contains(log.titles[0], "spotify") { // 標題點名平台,使用者才知道在看哪一邊
		t.Errorf("標題要點名平台:%q", log.titles[0])
	}
	if !strings.Contains(out, "t2") || !strings.Contains(out, "t3") || strings.Contains(out, "t1") {
		t.Fatalf("要顯示選中的清單(p2):%q", out)
	}
}

func TestPlLinkThreeStagePicker(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "t1")
	fs.set("p2", "冬日暖調", "t2")
	stubPickers(t, 0, 1, 0) // spotify → 冬日暖調 → 建立新的清單(清單一個都還沒有,索引 0 就是它)
	stubNewName(t, "我的最愛")
	n0 := fs.listCalls()
	out, _ := mustPull(t, "pl", "link")
	if !strings.Contains(out, "已連結 我的最愛(") || !strings.Contains(out, "spotify:p2") {
		t.Fatalf("三段挑選要連成:%q", out)
	}
	// 挑選器與下面的存在性檢查沿用同一份 refs:多打一趟不只是浪費,兩次之間清單被刪掉時
	// 使用者會拿到「不在你的清單列表裡」,指的原因與實際不符。
	if got := fs.listCalls() - n0; got != 1 {
		t.Fatalf("挑選器路徑只該讀一次 /me/playlists,實際 %d 次", got)
	}
	pl := decodeFile[canon.Playlist](t, driveFiles(t, dc), "pl__")
	if pl.Name != "我的最愛" || pl.Links["spotify"] != "p2" {
		t.Fatalf("Drive 上的清單:%+v", pl)
	}
}

func TestPlLinkPickerMarksTakenAndCancels(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "t1")
	fs.set("p2", "冬日暖調", "t2")
	mustPull(t, "pl", "link", "上班路上", "spotify:p1")
	before := driveFiles(t, dc)

	// 第三段的標籤要標出「p1 已經被誰佔走」,不然選了兩秒後才被 RunE 擋下。
	log := stubPickers(t, 0, 0, -1) // spotify → p1 → 取消
	if _, _, err := runPull(t, "pl", "link"); err == nil || err.Error() != "已取消" {
		t.Fatalf("取消要回「已取消」:%v", err)
	}
	if got := log.nth(2); len(got) != 2 || !strings.Contains(got[0], "已連結到這個清單") || got[1] != "+ 建立新的清單" {
		t.Fatalf("第三段要標出已連結、並提供建立新的清單:%v", got)
	}
	if !sameFiles(before, driveFiles(t, dc)) {
		t.Fatal("取消不可寫 Drive")
	}
	// 三段各自取消,都不可寫入。
	for stage, choose := range [][]int{{-1}, {0, -1}, {0, 1, -1}} {
		stubPickers(t, choose...)
		if _, _, err := runPull(t, "pl", "link"); err == nil || err.Error() != "已取消" {
			t.Fatalf("第 %d 段取消:%v", stage+1, err)
		}
		if !sameFiles(before, driveFiles(t, dc)) {
			t.Fatalf("第 %d 段取消不可寫 Drive", stage+1)
		}
	}
}

// 「+ 建立新的清單」要真的建新的:名字撞到既有清單時明講,不可靜靜 link 到舊的
// (使用者按的是建立,不是連結)。
func TestPlLinkNewNameCollides(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "t1")
	fs.set("p2", "冬日暖調", "t2")
	mustPull(t, "pl", "link", "上班路上", "spotify:p1")
	before := driveFiles(t, dc)

	// 撞到既有清單的「名字」與「pid」都要擋 —— 呼叫端拿這個字串去 s.find,兩種它都認,
	// 不擋就會變成「按了建立新的、卻被靜靜 link 到舊的那個」。
	pid := decodeFile[canon.Playlist](t, before, "pl__").PID
	for _, typed := range []string{"上班路上", pid} {
		stubPickers(t, 0, 1, 1) // spotify → p2 → +建立新的清單(既有一個,索引 1 是建立)
		stubNewName(t, typed)
		_, _, err := runPull(t, "pl", "link")
		if err == nil || !strings.Contains(err.Error(), "已經有這個清單了") {
			t.Fatalf("新名字打成 %q 要明講,不可默默連到舊的:%v", typed, err)
		}
		if !sameFiles(before, driveFiles(t, dc)) {
			t.Fatalf("打成 %q 不可寫 Drive", typed)
		}
	}
	// 名字不撞時照樣建新的,不會被既有清單吸走。
	stubPickers(t, 0, 1, 1)
	stubNewName(t, "冬日")
	out, _ := mustPull(t, "pl", "link")
	if !strings.Contains(out, "已連結 冬日(") {
		t.Fatalf("要建新的清單:%q", out)
	}
	n := 0
	for name := range driveFiles(t, dc) {
		if strings.HasPrefix(name, "pl__") {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("Drive 上該有兩個清單檔,實際 %d", n)
	}
}

// --all 與清單名同時給:TTY 下也維持同一句錯誤,且不開挑選器。
func TestNeedTargetBothGiven(t *testing.T) {
	pullWorld(t)
	origTTY, origPick := isInteractive, pickOne
	isInteractive = func(*cobra.Command) bool { return true }
	pickOne = func(string, []string) (int, error) {
		t.Fatal("兩個都給了不該開挑選器")
		return 0, nil
	}
	t.Cleanup(func() { isInteractive, pickOne = origTTY, origPick })
	for _, c := range []struct{ verb, want string }{
		{"pull", "指定一個清單(名稱或 pid),或用 --all 拉全部已連結的清單"},
		{"push", "指定一個清單(名稱或 pid),或用 --all 推全部已連結的清單"},
		{"sync", "指定一個清單(名稱或 pid),或用 --all 同步全部已連結的清單"},
	} {
		_, _, err := runPull(t, "pl", c.verb, "通勤", "--all")
		if err == nil || err.Error() != c.want {
			t.Errorf("pl %s 通勤 --all:%v,want %q", c.verb, err, c.want)
		}
	}
}

func TestPlUnlinkTwoStagePicker(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "t1")
	mustPull(t, "pl", "link", "上班路上", "spotify:p1")
	log := stubPickers(t, 0, 0)
	out, _ := mustPull(t, "pl", "unlink")
	if got := log.nth(0); len(got) != 1 || !strings.Contains(got[0], "上班路上") || !strings.Contains(got[0], "spotify:p1") {
		t.Fatalf("第一段要列已連結的清單與它連到哪:%v", got)
	}
	if got := log.nth(1); len(got) != 1 || got[0] != "spotify:p1" {
		t.Fatalf("第二段只列這個清單真的連了的平台:%v", got)
	}
	if !strings.Contains(out, "已取消連結 上班路上(") {
		t.Fatalf("unlink 輸出:%q", out)
	}
	if pl := decodeFile[canon.Playlist](t, driveFiles(t, dc), "pl__"); len(pl.Links) != 0 {
		t.Fatalf("連結要清掉:%+v", pl.Links)
	}
}

// pull / push / sync 的挑選器只列「已連結」的清單 —— 沒連結的選了也只會撞 pullTargets 的錯。
func TestPlPullPickerOnlyOffersLinked(t *testing.T) {
	fs, _, _ := pullWorld(t)
	fs.set("p1", "通勤", "t1")
	fs.set("p2", "冬日暖調", "t2")
	mustPull(t, "pl", "link", "上班路上", "spotify:p1")
	mustPull(t, "pl", "link", "沒連的", "spotify:p2")
	mustPull(t, "pl", "unlink", "沒連的", "spotify") // 留一個沒有任何連結的清單,它不該出現在挑選器裡
	log := stubPickers(t, 0, 0, 0)
	for i, verb := range []string{"pull", "push", "sync"} {
		if _, _, err := runPull(t, "pl", verb, "--dry-run"); err != nil && exitOf(t, err) == 1 {
			t.Fatalf("pl %s 挑選器:%v", verb, err)
		}
		if got := log.nth(i); len(got) != 1 || !strings.HasPrefix(got[0], "上班路上") {
			t.Fatalf("pl %s 只該列已連結的:%v", verb, got)
		}
		if !strings.Contains(log.titles[i], "選一個清單來") {
			t.Errorf("標題:%q", log.titles[i])
		}
	}
}

func TestPlPullPickerNoLinkedPlaylists(t *testing.T) {
	pullWorld(t)
	stubPickers(t)
	_, _, err := runPull(t, "pl", "pull")
	if err == nil || !strings.Contains(err.Error(), "先 capy pl link") {
		t.Fatalf("沒有已連結的清單要指路:%v", err)
	}
}
