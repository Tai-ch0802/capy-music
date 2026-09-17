package provider

import (
	"regexp"
	"strconv"
	"time"
)

// ISRCParts:ISRC 的四段(P7 決策 42,2026-09-17)。離線拆解、不查任何國碼 / 登記者對照表;只給頁面顯示。
type ISRCParts struct {
	Country     string // 前兩碼:國碼,或 Q? / ZZ 這類非地理前綴(見 Geographic)
	Registrant  string // 三碼登記者代碼
	Year        string // 兩碼年份
	Designation string // 五碼流水號
	YearFull    int    // 啟發式(頁面顯示請避免講得像事實):兩碼 ≤ 今年後兩碼 + 1 → 20xx,否則 19xx;+1 是年底為隔年發行預先配號的常態
	Geographic  bool   // false = 不是國家:nonGeographicPrefixes(QM / QT / QZ / QN 是發行商配到的、ZZ 是國際 ISRC 總部)
}

// nonGeographicPrefixes:明列而不是 Q 通配——QA(卡達)是正常國碼;IFPI 再配一支新的 Q? 出來會暫時落回「國家」
// (頁面 Intl.DisplayNames 原樣回代碼,R-10 手動驗收會撞到),誤標一個真實國家反而沒人會發現(review #58)。
var nonGeographicPrefixes = map[string]bool{"QM": true, "QT": true, "QZ": true, "QN": true, "ZZ": true}

// isrcPartsRe 比 NormalizeISRC 嚴:國碼兩個字母、年份與流水號是數字(ISRC 標準);登記者代碼英數。
var isrcPartsRe = regexp.MustCompile(`^([A-Z]{2})([A-Z0-9]{3})([0-9]{2})([0-9]{5})$`)

// ParseISRC:NormalizeISRC 後拆段;不合格(含年份 / 流水號不是數字)回 (ISRCParts{}, false)。
func ParseISRC(s string) (ISRCParts, bool) { return parseISRC(s, time.Now().Year()) }

func parseISRC(s string, thisYear int) (ISRCParts, bool) {
	m := isrcPartsRe.FindStringSubmatch(NormalizeISRC(s))
	if m == nil {
		return ISRCParts{}, false
	}
	p := ISRCParts{Country: m[1], Registrant: m[2], Year: m[3], Designation: m[4]}
	yy, _ := strconv.Atoi(p.Year) // regexp 已保證是兩位數字
	if yy <= thisYear%100+1 {     // +1:年底為隔年發行預先配號是常態;ISRC 1988 年才有,1927 那一側沒有真實案例
		p.YearFull = 2000 + yy
	} else {
		p.YearFull = 1900 + yy
	}
	p.Geographic = !nonGeographicPrefixes[p.Country]
	return p, true
}
