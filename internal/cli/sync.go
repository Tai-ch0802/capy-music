package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/ui"
)

// P5 T5:pl sync(spec 附錄 A、附錄 C 決策 31)= 同一把鎖裡每個清單先 pull 各平台、再 push 各平台;一張表(多一欄 DIR)、一次確認、
// 閾值各算;push 半邊重用 pull 半邊剛讀的 L(不再打一次平台),兩個 push 前提由「先 pull 後 push」建構保證。

var syncHeader = append([]string{"DIR"}, pullHeader...)

func newPlSyncCmd() *cobra.Command {
	var all, dryRun, yes, force bool
	var prov string
	cmd := &cobra.Command{
		Use:   "sync [name|pid]",
		Short: "先 pull 再 push 的一輪:一張表、一次確認(spec 決策 31)",
		Long: `同一把鎖裡每個清單先 pull 各平台(平台 → canonical)、再 push 各平台(canonical → 平台),--provider 只走一個平台。
變更集一次印完(非 TTY 是無標題 TSV:dir action provider playlist pos cid provider_id title artists reason;dir ∈ pull / push),確認一次;
--dry-run 的 push 半邊是用 pull 套用後的 canonical 投影的,看得到完整一輪。閾值對每個 (清單, 平台) 各算,exit code 同 pl pull / pl push。
push 半邊直接用 pull 半邊剛讀到的平台清單,不再讀一次;寫完平台才寫 Drive,Drive 那邊沒寫成時訊息會講明平台已經改了。
--provider 指到還寫不了的平台(Apple,P0-2 前)時只 pull 不 push,stderr 會說;某個清單的某個平台推不了(含 local file)也一樣只跳過那一格的 push 半邊,
不擋整輪(cron 的 sync --all 不會被一個清單綁死)。刪除閾值仍擋整輪。`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if all == (len(args) == 1) {
				return errors.New("指定一個清單(名稱或 pid),或用 --all 同步全部已連結的清單")
			}
			if prov != "" && !isProviderID(prov) {
				return fmt.Errorf("provider 為 %s:%q", strings.Join(providerIDs, "|"), prov)
			}
			if force && all {
				return errors.New("--force 只能配單一清單(capy pl sync <name> --force),不能配 --all")
			}
			ctx, stderr := cmd.Context(), cmd.ErrOrStderr()
			var deferred error
			applied, touched, pulled := 0, false, 0
			err := withCanonical(ctx, stderr, func(s *canonState) error {
				targets, err := pullTargets(s, args, all, prov)
				if err != nil {
					return err
				}
				pf := newPlatforms(ctx)
				pullRows, blocked, lives, err := observeAndDerive(ctx, s, targets, prov, stderr, pf)
				if err != nil {
					return err
				}
				plans, pushRows, pblocked, refused, err := planPush(ctx, s, targets, prov, stderr, pf, lives, false)
				if err != nil {
					return err
				}
				var rows [][]string
				for _, r := range pullRows {
					rows = append(rows, append([]string{"pull"}, r...))
				}
				for _, r := range pushRows {
					rows = append(rows, append([]string{"push"}, r...))
				}
				if len(rows) > 0 {
					ui.Table(cmd.OutOrStdout(), stdoutIsTTY(cmd), syncHeader, rows)
				}
				if len(refused) > 0 { // planPush 不 strict 時不會回 refused;留著是免得哪天有人改回 strict 而靜靜寫入
					return &BlockedError{Msg: strings.Join(refused, ";")}
				}
				if blocked = append(blocked, pblocked...); len(blocked) > 0 && !force {
					return &BlockedError{Msg: strings.Join(blocked, ";") + "。加 --force 越過(先用 --dry-run 看清楚要刪什麼)"}
				}
				resolveHint(s, targets, prov, stderr)
				ops := 0
				for _, p := range plans {
					ops += len(p.ops)
				}
				n := len(pullRows) + ops
				if n == 0 {
					fmt.Fprintln(stderr, "無變更")
					if dryRun {
						return errSkipCommit
					}
					return nil
				}
				if dryRun {
					return &PendingError{N: n}
				}
				if !yes {
					if !bothTTY(cmd) {
						return &PendingError{N: n}
					}
					ok, err := confirmWrite(fmt.Sprintf("套用以上 %d 筆變更(pull %d 筆到 Drive、push %d 筆到平台)?", n, len(pullRows), ops))
					if err != nil {
						return err
					}
					if !ok {
						return &PendingError{N: n}
					}
				}
				applied, touched, deferred = applyPlans(ctx, s, plans, stderr)
				if applied > 0 { // 平台寫入是既成事實,可以現在講;pull 那半是 Drive 的事,COMMIT 成功後才講
					fmt.Fprintf(stderr, "已推送 %d 筆變更\n", applied)
				}
				pulled = len(pullRows)
				return nil
			})
			if err == nil && pulled > 0 {
				fmt.Fprintf(stderr, "已套用 %d 筆 pull 變更\n", pulled)
			}
			return finishPush(err, applied, touched, deferred, "重跑 capy pl sync(pull 半邊會把已推到平台的變更當平台變更再吸收一次,不會重複)")
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "同步全部已連結的清單")
	cmd.Flags().StringVar(&prov, "provider", "", "只走這個 provider 的一輪(預設:清單連結的全部 provider)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "只列出整輪的變更,不碰平台也不碰 Drive(有變更時 exit 2)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "跳過確認(cron / 管線用)")
	cmd.Flags().BoolVar(&force, "force", false, "越過刪除閾值(pull 與 push 各算);只能配單一清單、不能配 --all")
	return cmd
}
