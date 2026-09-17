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
	YearFull    int    // 兩碼 ≤ 今年後兩碼 → 20xx,否則 19xx(ISRC 手冊的 rollover 啟發式)
	Geographic  bool   // false = 不是國家:Q 開頭(DistroKid / TuneCore / CD Baby 等發行商配到的 QM / QZ / QT…)或 ZZ(國際 ISRC 總部)
}

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
	if yy <= thisYear%100 {
		p.YearFull = 2000 + yy
	} else {
		p.YearFull = 1900 + yy
	}
	p.Geographic = p.Country[0] != 'Q' && p.Country != "ZZ"
	return p, true
}
