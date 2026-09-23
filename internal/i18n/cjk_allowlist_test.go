package i18n

// cjkAllowlist:還有中文字串沒搬進語系目錄的檔。Go 檔在 T2a–T2d 搬完;T2d 起網頁前端(internal/cli/webui 的 JS / HTML / CSS,
// 註解不算)也掃,剩下的就是它們,T3 搬。**只准縮小**:搬完一個檔就拿掉它,
// TestNoCJKOutsideCatalog 會抓「已經搬完卻還列著」與「沒列卻有中文」;清空之後 productionDefault 要改成 en。
// cjkAllowlistLen 釘住目前的長度:拿掉檔的 PR 要一起調小,白名單不准變長(TestCJKAllowlistOnlyShrinks)。
const cjkAllowlistLen = 13

var cjkAllowlist = []string{
	"internal/cli/webui/css/app.css",
	"internal/cli/webui/index.html",
	"internal/cli/webui/js/app.js",
	"internal/cli/webui/js/console.js",
	"internal/cli/webui/js/pages/account.js",
	"internal/cli/webui/js/pages/common.js",
	"internal/cli/webui/js/pages/doctor.js",
	"internal/cli/webui/js/pages/isrc.js",
	"internal/cli/webui/js/pages/move.js",
	"internal/cli/webui/js/pages/playlists.js",
	"internal/cli/webui/js/pages/search.js",
	"internal/cli/webui/js/pages/sync.js",
	"internal/cli/webui/js/player.js",
}
