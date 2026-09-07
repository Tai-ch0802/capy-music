package canon

import (
	"fmt"
	"strings"
)

// rankDigits:base62,ASCII 序即字典序,所以 rank 直接用字串比較。
const rankDigits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// RankBetween 回一個字典序嚴格落在 a 與 b 之間的 rank(fractional index,spec §6.2);a == "" 表示最前、
// b == "" 表示最後。產出永不以 '0' 結尾,否則 "x" 與 "x0" 之間插不進東西。
// 輸入來自 Drive 上的檔(別台裝置、未來版本、手改過的),所以要驗:非 base62 字元回錯;尾端的 '0' 不改變值
// (spec §6.2 的範例鍵 "a0" 就有),比較前先去掉,於是 ("a", "a0") 這種中間沒有空間的組合會回錯而不是回出界的值。
// ponytail: 尾端連續 Append 大約每 6 筆長 1 字,首次匯入用 Ranks(n) 均分;鍵真的長到礙眼再換
// rocicorp 那套帶整數部分長度前綴的演算法。
func RankBetween(a, b string) (string, error) {
	for _, s := range []string{a, b} {
		if strings.Trim(s, rankDigits) != "" {
			return "", fmt.Errorf("rank 含非 base62 字元:%q", s)
		}
	}
	a = strings.TrimRight(a, "0")
	if b != "" {
		if b = strings.TrimRight(b, "0"); b == "" || a >= b {
			return "", fmt.Errorf("rank 順序錯或中間沒有空間:%q 與 %q", a, b)
		}
	}
	n := len(rankDigits)
	digit := func(s string, i, def int) int {
		if i < len(s) {
			return strings.IndexByte(rankDigits, s[i])
		}
		return def
	}
	var out []byte
	for i := 0; ; i++ {
		x, y := digit(a, i, 0), digit(b, i, n)
		if x == y {
			out = append(out, rankDigits[x])
			continue
		}
		if y-x > 1 {
			return string(append(out, rankDigits[(x+y)/2])), nil
		}
		// y == x+1:沿著 a 走,之後只要比 a 的剩餘部分大就一定比 b 小。
		out = append(out, rankDigits[x])
		for j := i + 1; ; j++ {
			z := digit(a, j, 0)
			if z+1 < n {
				return string(append(out, rankDigits[(z+n)/2])), nil
			}
			out = append(out, rankDigits[z])
		}
	}
}

// Ranks 產生 n 個均分、已排序的 rank(首次匯入整個清單用),長度只夠區分 n 個。
func Ranks(n int) []string {
	if n <= 0 {
		return nil
	}
	length, total := 1, len(rankDigits)
	for total <= n {
		length++
		total *= len(rankDigits)
	}
	step := total / (n + 1)
	out := make([]string, n)
	for k := 1; k <= n; k++ {
		v, buf := k*step, make([]byte, length)
		for i := length - 1; i >= 0; i-- {
			buf[i], v = rankDigits[v%len(rankDigits)], v/len(rankDigits)
		}
		out[k-1] = strings.TrimRight(string(buf), "0")
	}
	return out
}
