package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/secret"
)

// seedSpotifyAuthorizedAt:keychain 裡放一顆 Spotify token,授權時間是 at(零值 = 不知道,這個版本之前登入的)。
func seedSpotifyAuthorizedAt(t *testing.T, at time.Time) {
	t.Helper()
	rec := map[string]any{"access_token": "at", "token_type": "Bearer", "refresh_token": "rt", "expiry": time.Now().Add(time.Hour), "issued_at": time.Now()}
	if !at.IsZero() {
		rec["authorized_at"] = at
	}
	b, _ := json.Marshal(rec)
	if err := secret.Set(auth.KeySpotifyToken, string(b)); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(&config.Config{SpotifyClientID: strings.Repeat("ab", 16)}); err != nil {
		t.Fatal(err)
	}
}

// TestSpotifyRenewalShown:【決策 56】auth status 說 Spotify 的登入約何時失效(授權 + 180 天);剩 10 天以內叫人重新登入;
// 過了就說已失效;不知道授權時間就照實說不知道,不猜。--json 給 UTC 的 refresh_token_expiry(不知道就不給)。
func TestSpotifyRenewalShown(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ago     time.Duration // 多久以前授權;0 = 不知道
		want    string
		hasJSON bool
	}{
		{"known", 30 * 24 * time.Hour, "(剩 149 天)", true},
		{"soon", 175 * 24 * time.Hour, "請在那之前執行 capy auth login spotify", true},
		{"expired", 181 * 24 * time.Hour, "大概已在", true},
		{"unknown", 0, "不知道(用舊版的 capy 登入的", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setCLITestConfig(t)
			t.Cleanup(keyring.MockInit)
			var at time.Time
			if tc.ago > 0 {
				at = time.Now().Add(-tc.ago)
			}
			seedSpotifyAuthorizedAt(t, at)
			out, err := runCLI(t, "auth", "status")
			if err != nil || !strings.Contains(out, "authorization: ") || !strings.Contains(out, tc.want) {
				t.Errorf("auth status 要說登入何時失效(%q):%v\n%s", tc.want, err, out)
			}
			js, err := runCLI(t, "auth", "status", "--json")
			if err != nil {
				t.Fatal(err)
			}
			var st struct {
				Spotify struct {
					RefreshTokenExpiry string `json:"refresh_token_expiry"`
				} `json:"spotify"`
			}
			if err := json.Unmarshal([]byte(js), &st); err != nil {
				t.Fatal(err)
			}
			got := st.Spotify.RefreshTokenExpiry
			if tc.hasJSON {
				exp, err := time.Parse(time.RFC3339, got)
				if err != nil || !strings.HasSuffix(got, "Z") || exp.Sub(at.Add(auth.SpotifyRefreshLifetime)).Abs() > time.Second {
					t.Errorf("refresh_token_expiry 要是 UTC 的「授權 + 180 天」:%q %v", got, err)
				}
			} else if got != "" {
				t.Errorf("不知道授權時間就不給:%q", got)
			}
		})
	}
}

// TestSpotifyRenewalBoundary:剩剛好 10 天就開始叫人重新登入(第 170 天);剩 10 天多一點還不叫。
func TestSpotifyRenewalBoundary(t *testing.T) {
	setCLITestConfig(t)
	t.Cleanup(keyring.MockInit)
	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	seedSpotifyAuthorizedAt(t, at)
	day170 := at.Add(auth.SpotifyRefreshLifetime - auth.SpotifyRenewWarn)
	if s := spotifyRenewalText(day170); !strings.Contains(s, "請在那之前") {
		t.Errorf("剩剛好 10 天要叫人重新登入:%s", s)
	}
	if s := spotifyRenewalText(day170.Add(-time.Minute)); strings.Contains(s, "請在那之前") {
		t.Errorf("剩 10 天多一點還不叫:%s", s)
	}
	if s := spotifyRenewalText(at.Add(auth.SpotifyRefreshLifetime)); !strings.Contains(s, "左右失效") {
		t.Errorf("到期那一刻就算失效:%s", s)
	}
}

// TestDoctorSpotifyRenewal:doctor 的 refresh token 那一項:過期就不通過(refresh 一定失敗);快到了照樣通過但叫人重新登入;
// 不知道授權時間跟以前一樣。
func TestDoctorSpotifyRenewal(t *testing.T) {
	for _, tc := range []struct {
		ago    time.Duration
		wantOK bool
		want   string
	}{
		{30 * 24 * time.Hour, true, "剩 149 天"},
		{175 * 24 * time.Hour, true, "請在那之前執行 capy auth login spotify"},
		{181 * 24 * time.Hour, true, "大概已在"}, // 180 天是保守估計:只說大概、照樣通過,真的死了沒由下一項實際換發來判
		{0, true, ""},
	} {
		t.Run(fmt.Sprint(tc.ago), func(t *testing.T) {
			setCLITestConfig(t)
			t.Cleanup(keyring.MockInit)
			var at time.Time
			if tc.ago > 0 {
				at = time.Now().Add(-tc.ago)
			}
			seedSpotifyAuthorizedAt(t, at)
			detail, err := checkRefreshToken(context.Background())
			if (err == nil) != tc.wantOK {
				t.Fatalf("通過與否:%v %q", err, detail)
			}
			msg := detail
			if err != nil {
				msg = err.Error()
			}
			if !strings.Contains(msg, tc.want) {
				t.Errorf("要說 %q:%q", tc.want, msg)
			}
		})
	}
}
