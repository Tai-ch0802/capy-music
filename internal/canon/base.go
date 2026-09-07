package canon

// MergeBase 合併所有裝置檔的 base:每個 (pid, provider) 取 observed_at 最大者(LWW register);
// 同秒時取 device_id 較大者——沒有決定性 tiebreak,兩台裝置會各算各的 base、永不收斂。
// 結果與輸入順序無關。
func MergeBase(devs []DeviceState) map[string]map[string]Base {
	out := map[string]map[string]Base{}
	winner := map[string]map[string]string{} // 目前勝出的 device_id
	for _, d := range devs {
		for pid, byProv := range d.Base {
			for prov, b := range byProv {
				if cur, ok := out[pid][prov]; ok {
					if cur.ObservedAt > b.ObservedAt || (cur.ObservedAt == b.ObservedAt && winner[pid][prov] >= d.DeviceID) {
						continue
					}
				}
				if out[pid] == nil {
					out[pid], winner[pid] = map[string]Base{}, map[string]string{}
				}
				out[pid][prov], winner[pid][prov] = b, d.DeviceID
			}
		}
	}
	return out
}
