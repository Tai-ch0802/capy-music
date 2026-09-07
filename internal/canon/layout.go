package canon

// Drive appdata 的扁平佈局(spec §6.3):檔名 + appProperties(kind / pid / device_id),用 files.list 的 q 過濾。
const (
	KindManifest = "manifest"
	KindTracks   = "tracks"
	KindPlaylist = "playlist"
	KindDevice   = "device"
)

// FileRef:檔名與 appProperties,直接餵 drive.Client 的 Find / Create。
type FileRef struct {
	Name  string
	Props map[string]string
}

func ManifestFile() FileRef { return FileRef{"manifest.json", map[string]string{"kind": KindManifest}} }
func TracksFile() FileRef   { return FileRef{"tracks.json", map[string]string{"kind": KindTracks}} }
func PlaylistFile(pid string) FileRef {
	return FileRef{"pl__" + pid + ".json", map[string]string{"kind": KindPlaylist, "pid": pid}}
}
func DeviceFile(deviceID string) FileRef {
	return FileRef{"dev__" + deviceID + ".json", map[string]string{"kind": KindDevice, "device_id": deviceID}}
}
