package cli

// reportProgress:長命令的真實進度(P8 決策 47)。預設 no-op——終端機與非 TTY 的輸出一個位元組不改;
// `capy --web` 在 installWebSeams 換成送 progress 事件({stage, done, total}),頁面的進度條只吃這個,
// 沒有事件就不畫進度條(不編百分比,同 #65)。total == 0 = 只是階段的標記,沒有可數的東西。
// stage:read(讀來源清單)/ match(逐首比對;planResolve,`capy resolve` 也會經過)/ write(寫入目的地)。
var reportProgress = func(stage string, done, total int) {}
