package provider

import "testing"

func TestParseISRC(t *testing.T) {
	tw := ISRCParts{Country: "TW", Registrant: "K23", Year: "16", Designation: "80790", YearFull: 2016, Geographic: true}
	for _, tc := range []struct {
		in   string
		ok   bool
		want ISRCParts
	}{
		{"TWK231680790", true, tw},
		{"twk-2316-80790", true, tw}, // 小寫、連字號:同 NormalizeISRC
		{"QM6MZ2000001", true, ISRCParts{Country: "QM", Registrant: "6MZ", Year: "20", Designation: "00001", YearFull: 2020}}, // 發行商前綴,不是國家
		{"ZZABC2100001", true, ISRCParts{Country: "ZZ", Registrant: "ABC", Year: "21", Designation: "00001", YearFull: 2021}}, // 國際 ISRC 總部
		{"USRC19900001", true, ISRCParts{Country: "US", Registrant: "RC1", Year: "99", Designation: "00001", YearFull: 1999, Geographic: true}},
		{"USRC12600001", true, ISRCParts{Country: "US", Registrant: "RC1", Year: "26", Designation: "00001", YearFull: 2026, Geographic: true}}, // 今年
		{"USRC12700001", true, ISRCParts{Country: "US", Registrant: "RC1", Year: "27", Designation: "00001", YearFull: 2027, Geographic: true}}, // 明年:年底預先配號
		{"USRC12800001", true, ISRCParts{Country: "US", Registrant: "RC1", Year: "28", Designation: "00001", YearFull: 1928, Geographic: true}}, // 後年 → 上世紀
		{"QAABC2100001", true, ISRCParts{Country: "QA", Registrant: "ABC", Year: "21", Designation: "00001", YearFull: 2021, Geographic: true}}, // 卡達是國家,Q 通配會誤傷
		{"QNABC2100001", true, ISRCParts{Country: "QN", Registrant: "ABC", Year: "21", Designation: "00001", YearFull: 2021}},                   // 明列的發行商前綴
		{"nope", false, ISRCParts{}},
		{"TWK23168079", false, ISRCParts{}},  // 11 碼
		{"TWK23A680790", false, ISRCParts{}}, // 年份不是數字
		{"1WK231680790", false, ISRCParts{}}, // 國碼不是字母
	} {
		got, ok := parseISRC(tc.in, 2026)
		if ok != tc.ok || got != tc.want {
			t.Errorf("%q → %+v ok=%v,要 %+v ok=%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
	if p, ok := ParseISRC("TWK231680790"); !ok || p.YearFull != 2016 {
		t.Errorf("ParseISRC 用今年:%+v ok=%v", p, ok)
	}
}
