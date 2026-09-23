package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/i18n"
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
		Short: i18n.T("cmd.pl.sync.short"),
		Long:  i18n.T("cmd.pl.sync.long"),
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := needTarget(cmd, args, all, i18n.Errorf("pick.err.need_target.sync")); err != nil {
				return err
			}
			if prov != "" && !isProviderID(prov) {
				return i18n.Errorf("push.err.bad_provider", "valid", strings.Join(providerIDs, "|"), "value", strconv.Quote(prov))
			}
			if force && all {
				return i18n.Errorf("sync.err.force_with_all")
			}
			ctx, stderr := cmd.Context(), cmd.ErrOrStderr()
			var deferred error
			applied, touched, pulled := 0, false, 0
			err := withCanonical(ctx, stderr, func(s *canonState) error {
				targets, err := pullTargets(s, args, all, prov, i18n.T("pull.pick.title.sync"))
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
					if err := ui.Table(cmd.OutOrStdout(), stdoutIsTTY(cmd), syncHeader, rows, tableOpts(yes)...); err != nil {
						return err
					}
				}
				if len(refused) > 0 { // planPush 不 strict 時不會回 refused;留著是免得哪天有人改回 strict 而靜靜寫入
					return &BlockedError{Msg: strings.Join(refused, i18n.T("sep.clause"))}
				}
				if blocked = append(blocked, pblocked...); len(blocked) > 0 && !force {
					return &BlockedError{Msg: i18n.T("changeset.blocked", "reasons", strings.Join(blocked, i18n.T("sep.clause")))}
				}
				resolveHint(s, targets, prov, stderr)
				ops := 0
				for _, p := range plans {
					ops += len(p.ops)
				}
				n := len(pullRows) + ops
				if n == 0 {
					fmt.Fprintln(stderr, i18n.T("changeset.no_changes"))
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
					prompt := i18n.T("sync.confirm", "count", n, "pulls", len(pullRows), "pushes", ops)
					if force {
						prompt = i18n.T("sync.confirm_force", "prompt", prompt)
					}
					ok, err := confirmWrite(prompt)
					if err != nil {
						return err
					}
					if !ok {
						return &PendingError{N: n}
					}
				}
				applied, touched, deferred = applyPlans(ctx, s, plans, stderr)
				if applied > 0 { // 平台寫入是既成事實,可以現在講;pull 那半是 Drive 的事,COMMIT 成功後才講
					fmt.Fprintln(stderr, i18n.T("push.done", "count", applied))
				}
				pulled = len(pullRows)
				return nil
			})
			if err == nil && pulled > 0 {
				fmt.Fprintln(stderr, i18n.T("sync.done_pull", "count", pulled))
			}
			return finishPush(err, applied, touched, deferred, i18n.T("sync.next"), i18n.T("sync.next_after_half"))
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, i18n.T("cmd.pl.sync.flag.all"))
	cmd.Flags().StringVar(&prov, "provider", "", i18n.T("cmd.pl.sync.flag.provider"))
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, i18n.T("cmd.pl.sync.flag.dry_run"))
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, i18n.T("cmd.pl.sync.flag.yes"))
	cmd.Flags().BoolVar(&force, "force", false, i18n.T("cmd.pl.sync.flag.force"))
	return cmd
}
