# 清單順序不可破壞 / `pl dedup` / `capy migrate`(2026-09-15)

使用者:「歌單的歌曲順序很重要,這隱含著使用者的加入歌單記憶。歌單裡的順序絕對不能被破壞。還有希望加入新的功能,
可以針對歌單裡去除重複出現的項目。根據各平台的歌曲 id 去重。然後如果 ISRC 已經有相同的,則略過。最後再多一個 migrate
指令,可以引導使用者直接將 A 平台的音樂清單搬移(或新增)至 B 平台上,就不用讓使用者在自己理解 pull, push, link 等指令關聯。」

三件事、兩個 PR:(1) 順序硬約束 + `pl dedup`(這個 PR);(2) `capy migrate`(下一個 PR)。
使用者定案(2026-09-15):重複 = 同平台 id **或同 ISRC**都算、拿掉後面那份;migrate 完成後 A 不連結、只有 B 連著 canonical;
沒有實際看過順序被打亂(所以不是 bug hunt,是把約束寫下來並守住)。

---

## 0. 現況(查證過)

- 順序在 canonical 模型裡已經是一級公民:item 有 rank;DERIVE 規則 6′ 只在平台真的重排時才採平台順序、規則 4′ 尾端新增接在 C 尾端;
  push 以 cid 對齊(LCS)、目標序列 = C 的順序;Spotify `ApplyOps` 整批取代;模型測試的 oracle 是「意圖」。
  **精確說法**:沒動到的項目相對順序永遠不變;只有兩邊都重排時才由後 pull 者勝(決策 26 接受的平手)。
  **沒有 base 的 bootstrap pull 會採平台順序**(規則 6′ 只在有 base 時生效)—— migrate 不能靠它(§3)。
- cid:有 ISRC 的曲目 `i:<ISRC>`(跨平台同一首),沒有的 `p:<平台>:<id>`;比對前先 `id.Redirect`(合併墓碑)。
  所以「同 cid 出現兩次」= 同平台 id 或同 ISRC,正是使用者要的重複定義。
- 寫入能力:Spotify 可建清單、增刪換序改名;local 可增刪換序(不可建);**Apple 只讀**(等維護者 R-8 → P5 T6)。
  使用者的清單多在 Apple:去重的「移除」與 migrate 的「目標」在 Apple 上今天都做不到,只能報告 / 當來源。
- Spotify 建清單(`POST /me/playlists`)與推曲目(`PUT /items`)**還沒在真帳號驗過**(README 有警示、維護者待辦);migrate 的可用性卡在那個 smoke test。
- 刪除路徑硬約束:dry-run + `removalBlocked`(>10 首、或 >30% 且 >3 首)+ 確認。

## 1. 順序:寫成硬約束,不重做同步邏輯(這個 PR)

- CLAUDE.md 硬約束加一條、ARCHITECTURE §6.6 加一列、附錄 C 決策 38、README 與指南各一句:
  「清單順序是使用者的記憶。任何路徑都不得排序或打亂清單;沒動到的項目相對順序永遠不變;去重只拿掉後出現的那份;
  搬移只在尾端新增;只有平台自己重排了才採平台順序。」
- 守住它的測試:`canon.Duplicates` 的性質測試(拿掉指出的位置後沒有重複、剩下的是原序列的子序列、每個鍵保留第一份、空鍵不動);
  CLI 的回歸測試斷言去重後保留的 item(iid / rank / added_at)逐位元不變。migrate 那個 PR 再加「B 原順序是輸出的前綴」。

## 2. `capy pl dedup`(這個 PR;`internal/cli/dedup.go`、`internal/canon/dedup.go`)

一個純函式兩條路徑共用:`canon.Duplicates(keys)` 回第二次以後出現的位置與它重複到的第一份;鍵 = capy 認定為同一首的東西。

- **`<provider>:<清單 ID 或名稱>`:只報告。** 直接讀平台清單(同 `pl show` 的讀法),鍵 = `canon.CID(prov, id, isrc)` 的公式
  (同 id 或同 ISRC 就是同一首),表 `POS ID TITLE ARTISTS REASON`(reason 寫「與 pos N 同 id / 同 ISRC …」,pos 從 0 起、指向保留的那份),
  非 TTY 是無標題 TSV;不碰 Drive、不需要連結;有沒有重複 exit 都是 0(報告就是報告,同 `resolve` 的佇列)。stderr 依平台能不能寫指路:
  可寫 → `pl link` 再 `pl dedup <名稱>`;只讀(Apple)→ 照表在 app 裡手動刪。`--yes` / `--force` / `--provider` 配這種寫法是錯誤(exit 1):
  帶著寫入形狀的 flag 卻只報告、exit 0,會讓人以為刪了。
- **canonical 清單(name|pid;不帶參數 + TTY 開挑選器):sync 的一輪中間多一步。** `observeAndDerive`(pull 半邊,push 的兩個前提靠它)
  → `dedupCanonical`(C 裡 `Redirect(cid)` 第二次以後出現的拿掉,保留第一份,其餘 item 與 rank 一個都不動)→ `planPush`(不 strict,同 sync;
  C 只剩一份、L 有兩份,LCS 配不到的那份就是 `remove`)。一張表 `syncHeader`,`dir` 多一種 `dedup`(provider 空、pos 是 C 的位置);
  閾值去重(分母 C 的曲數)與 push(分母 L 的長度)各算,`--force` 同 sync 的爆炸半徑(去掉的份推到每個可寫平台);exit code 同 sync。
  - **C 沒有重複、每個平台的 L 也沒有 → 「沒有重複」、`errSkipCommit`、零寫入**:pull 半邊看到的其他變更留給 `pl sync`,dedup 不退化成 sync。
  - **C 早已去重、只讀的平台還留著多的份**:規則 4′ 不會把它加回來(L 兩份、base 兩份、C 一份 → 允許新增 0),所以「沒有重複」會誤導。
    `platformDuplicates` 對 pull 半邊讀到的每個 L 再算一次(`canon.Observe` 純函式),沒有 push 計畫的那些(寫不了、這次推不了)列 stderr
    「apple:<id> 還有 N 份重複(pos …),請在 app 裡手動刪除」;使用者刪掉後 `pl sync` 零變更(規則 5:C 已經只有一份、配對上,不會再移除)。
  - COMMIT 失敗而平台已改:`finishPush` 的說法改成「重跑 capy pl dedup 或 capy pl sync」——base 沒前進,重跑的 pull 半邊會把平台上已拿掉的那份
    當平台變更吸收(規則 5),C 自然去重。
- **已知取捨:同 ISRC 不同 id**(單曲版 / 專輯版)。C 記第一份、mapping 是第一次觀測到的 id;平台上留哪個 id 由 push 的 LCS 配對決定——
  `lcsPairs` 對同一個 C item 取 L 順序最後的候選,相鄰兩份時留**後面**那個 id。曲目一樣、順序一樣、只差 id;測試釘住。要「留第一個 id」得改
  `lcsPairs` 的平手規則(Derive 與 PushPlan 共用,重複曲目的配對規則是 PR #19 review 定的),等有人需要再動。
- 一次一個清單;沒有 `--all`(去重是使用者盯著看的事,不是 cron 的事)。
- 測試:`TestDuplicates*`(性質)、`TestPlDedup*`(報告路徑含 restricted / 只讀;canonical 一輪含 dry-run / 非 TTY / 零寫入 / 保留的 item 逐位元不變 /
  再跑「沒有重複」;平台把重複加回來;同 ISRC 不同 id;只讀平台手動 + 之後 sync 不加回 + 手動刪後收斂;閾值 + `--force`;參數與 help)。
  假平台加 `aliasISRC`(兩個 id 共用一個 ISRC)。

## 3. `capy migrate <A 清單> --from <A> --to <B[:清單]>`(下一個 PR)

- **不靠 derive,順序明確建**:
  - `--to spotify`(新建):`pl link --create` 的核心建同名私人空清單並連結 → C = A 的曲目、A 的原順序。
  - `--to spotify:<既有>` / `--to local:<檔>`(加進既有):先把 B 現況 pull 進 C(C = B 原樣)→ 把 A 裡 **C 還沒有的**(`Redirect(cid)` 比;
    同 ISRC / 同 id 就算有 = 「已經有相同的,則略過」)**依 A 的順序接在尾端**。性質測試:B 原順序是輸出的前綴、追加的部分是 A 的子序列。
  - **A 不連結 canonical**(使用者定案):直接讀 A 的清單、只借 `Observe` 拿 cid 與 mapping。一次性複製,沒有 base 的包袱,之後 `pl sync`
    不會因 bootstrap 把 B 重排成 A 的順序。要持續同步的人走 README 的 link + sync 流程(結尾指路)。
  - `resolve --provider B`(ISRC 反查 → 模糊比對 ≥85 自動;其餘 TTY 接 `--review`,非 TTY 印佇列、只推有對應的)→ `push --provider B`
    (變更集全是 add、順序 = C,確認才推;`--yes` 跳過)。**不動 A 的任何東西。**
- **組合方式**:一個 `withCanonical` 裡呼叫 link / observe / resolve / push 既有的核心函式(要把它們從各自的 RunE 抽成可呼叫的函式)——
  一次 COMMIT、一次確認、原子;不像介面那樣連跑五個子命令(五趟 Drive、中途死掉留半成品、每步各問一次)。代價:PR 較大。
- 非 TTY:`--from` / `--to` 必填、`--yes` 才寫;錯誤訊息一字不變。目標限制:Apple 不能當目標(只讀);local 只能加進既有檔。頂層 `capy migrate`。
- 可用性卡在維護者的 Spotify 真帳號 smoke test(建清單 + 推曲目);Apple 當目標等 R-8 / T6。

## 4. 產出
- PR (1) `feat/pl-dedup`:§1 + §2,附 ARCHITECTURE v0.10、README、指南。
- PR (2) `feat/migrate`:§3,合併 (1) 之後開。
