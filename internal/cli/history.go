package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/cache"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/store"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

const (
	historyBusy         = 200 * time.Millisecond
	historyDefaultLimit = 50
)

var (
	historyHeader    = []string{"PLAYED_AT", "PROVIDER", "ID", "TITLE", "ARTISTS", "ALBUM"}
	historyTTYHeader = []string{"PLAYED_AT", "PROVIDER", "TITLE", "ARTISTS", "ALBUM"}
)

type historyRow struct {
	playedAt time.Time
	provider string
	id       string
	title    string
	artists  string
	album    string
}

func newHistoryCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "history",
		Short: i18n.T("cmd.history.short"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runHistoryList(cmd, limit)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", historyDefaultLimit, i18n.T("history.flag.limit"))

	var listLimit int
	listCmd := &cobra.Command{
		Use:   "list",
		Short: i18n.T("history.list.short"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runHistoryList(cmd, listLimit)
		},
	}
	listCmd.Flags().IntVar(&listLimit, "limit", historyDefaultLimit, i18n.T("history.flag.limit"))

	clearCmd := &cobra.Command{
		Use: "clear", Short: i18n.T("cmd.history.clear.short"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := cache.Load()
			c.ClearRecent()
			if err := c.Save(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), i18n.T("history.cleared", "count", int(c.Cleared())))
			return nil
		},
	}

	cmd.AddCommand(listCmd, clearCmd)
	return cmd
}

func runHistoryList(cmd *cobra.Command, limit int) error {
	if limit <= 0 {
		return i18n.Errorf("history.err.bad_limit", "value", strconv.Itoa(limit))
	}
	items, err := loadHistoryRows(limit)
	if err != nil {
		return err
	}
	tty := stdoutIsTTY(cmd)
	if tty {
		rows := make([][]string, len(items))
		for i, it := range items {
			ts := ""
			if !it.playedAt.IsZero() {
				ts = it.playedAt.Local().Format("2006-01-02 15:04")
			}
			rows[i] = []string{ts, it.provider, it.title, it.artists, it.album}
		}
		return ui.Table(cmd.OutOrStdout(), true, historyTTYHeader, rows)
	}
	rows := make([][]string, len(items))
	for i, it := range items {
		ts := ""
		if !it.playedAt.IsZero() {
			ts = it.playedAt.UTC().Format(time.RFC3339)
		}
		rows[i] = []string{ts, it.provider, it.id, it.title, it.artists, it.album}
	}
	return ui.Table(cmd.OutOrStdout(), false, historyHeader, rows)
}

func loadHistoryRows(limit int) ([]historyRow, error) {
	st, err := store.OpenReadOnly(historyBusy)
	if errors.Is(err, store.ErrNoDB) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer st.Close()

	journal, err := st.ListPlayJournal(limit)
	if err != nil {
		return nil, err
	}
	if len(journal) > 0 {
		out := make([]historyRow, len(journal))
		for i, e := range journal {
			out[i] = historyRow{
				playedAt: e.PlayedAt,
				provider: e.Provider,
				id:       e.ProviderID,
				title:    e.Title,
				artists:  strings.Join(e.Artists, ", "),
				album:    e.Album,
			}
		}
		return out, nil
	}

	_, recent, err := st.LoadCache()
	if err != nil {
		return nil, err
	}
	var out []historyRow
	for _, r := range recent {
		if r.Type == cache.TypeQuery {
			continue
		}
		var at time.Time
		if r.At > 0 {
			at = time.Unix(r.At, 0)
		}
		row := historyRow{
			playedAt: at,
			provider: r.Provider,
			id:       r.ID,
			title:    r.Label,
		}
		switch r.Type {
		case cache.TypeTrack:
			row.artists, row.album, _ = strings.Cut(r.Detail, " · ")
		case cache.TypeArtist:
			row.artists = r.Label
		}
		out = append(out, row)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}
