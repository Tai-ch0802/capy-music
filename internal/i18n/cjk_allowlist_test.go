package i18n

// cjkAllowlist:還有中文字串沒搬進語系目錄的檔。T2a–T3(2026-09-23)全部搬完,白名單是空的,之後也要保持空的:
// 使用者看得到的字一律進 locales/(en 與 zh-TW 同時補)。TestNoCJKOutsideCatalog 掃非測試的 Go 檔與
// internal/cli/webui 的 JS / HTML / CSS(註解不算);cjkAllowlistLen 釘住長度,白名單不准變長(TestCJKAllowlistOnlyShrinks)。
const cjkAllowlistLen = 0

var cjkAllowlist = []string{}
