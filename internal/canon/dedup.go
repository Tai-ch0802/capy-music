package canon

// pl dedup 的純函式核心(2026-09-15)。清單順序是使用者的記憶(CLAUDE.md 硬約束):去重只指出「多出來的那幾份」,
// 保留每一首第一次出現的那份,其餘 item 的相對順序與 rank 一個都不動;呼叫端把指出來的位置拿掉就是結果。

// Dup:序列裡第二次以後出現的一份——Pos 是它的位置,Keep 是它重複到的第一次出現(被保留的那份)。
type Dup struct{ Pos, Keep int }

// Duplicates 找出 keys 裡第二次以後出現的位置(依位置遞增),每個鍵保留第一次出現的那份。不改 keys、不排序。
// 鍵是「capy 認定為同一首」的東西:canonical 用經墓碑重導的 cid,平台清單用 CID(prov, id, isrc) 的公式——
// 兩邊都是「同平台 id 或同 ISRC 就是同一首」。空鍵不算重複(沒有東西可以認定它們是同一首)。
func Duplicates(keys []string) []Dup {
	first := map[string]int{}
	var dups []Dup
	for i, k := range keys {
		if k == "" {
			continue
		}
		if j, ok := first[k]; ok {
			dups = append(dups, Dup{Pos: i, Keep: j})
			continue
		}
		first[k] = i
	}
	return dups
}
