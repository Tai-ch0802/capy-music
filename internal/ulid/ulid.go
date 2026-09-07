// Package ulid 產生 ULID(48-bit 毫秒時戳 + 80-bit 隨機,Crockford base32 26 字):device_id、
// 之後 canonical model 的 pid / cid 都用它(spec §6.2)。手寫而不加依賴:編碼就是把 128-bit 當一個大整數
// 以 MSB 優先轉 base32,與 oklog/ulid 的輸出相同;同毫秒內不保證單調(這裡的用途不需要)。
package ulid

import (
	"crypto/rand"
	"errors"
	"math/big"
	"strings"
	"time"
)

const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// Now 是時戳來源。測試替換點。
var Now = time.Now

// New 回傳一個新的 ULID。
func New() string {
	var b [16]byte
	ms := uint64(Now().UnixMilli())
	for i := 5; i >= 0; i-- {
		b[i] = byte(ms)
		ms >>= 8
	}
	if _, err := rand.Read(b[6:]); err != nil {
		panic("ulid: crypto/rand 失敗:" + err.Error())
	}
	n := new(big.Int).SetBytes(b[:])
	out := make([]byte, 26)
	mask := big.NewInt(31)
	for i := 25; i >= 0; i-- {
		out[i] = alphabet[new(big.Int).And(n, mask).Int64()]
		n.Rsh(n, 5)
	}
	return string(out)
}

// Time 解出 ULID 的時戳(毫秒精度)。格式不對回錯。
func Time(s string) (time.Time, error) {
	if len(s) != 26 {
		return time.Time{}, errors.New("ulid: 長度必須是 26")
	}
	n := new(big.Int)
	for _, c := range strings.ToUpper(s) {
		idx := strings.IndexRune(alphabet, c)
		if idx < 0 {
			return time.Time{}, errors.New("ulid: 含非 Crockford base32 字元")
		}
		n.Lsh(n, 5)
		n.Or(n, big.NewInt(int64(idx)))
	}
	if n.BitLen() > 128 {
		return time.Time{}, errors.New("ulid: 超出 128 bit")
	}
	ms := new(big.Int).Rsh(n, 80).Int64()
	return time.UnixMilli(ms), nil
}
