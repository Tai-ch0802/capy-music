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
  可寫 → `pl link` 再 `pl dedup <名稱>`;只讀(Apple)→ 照表在 app 裡手動刪。`--yes` / `--force` / `--dry-run` / `--provider` 配這種寫法是錯誤(exit 1):
  帶著寫入形狀的 flag 卻只報告、exit 0,會讓人以為刪了;`--dry-run` 反過來會讓人以為沒有重複(canonical 路徑有重複是 exit 2;PR #54 review)。
- **canonical 清單(name|pid;不帶參數 + TTY 開挑選器):sync 的一輪中間多一步。** `observeAndDerive`(pull 半邊,push 的兩個前提靠它)
  → `dedupCanonical`(C 裡 `Redirect(cid)` 第二次以後出現的拿掉,保留第一份,其餘 item 與 rank 一個都不動)→ `planPush`(不 strict,同 sync;
  C 只剩一份、L 有兩份,LCS 配不到的那份就是 `remove`)。一張表 `syncHeader`,`dir` 多一種 `dedup`(provider 空、pos 是 C 的位置);
  閾值去重(分母 C 的曲數)與 push(分母 L 的長度)各算,`--force` 同 sync 的爆炸半徑(去掉的份推到每個可寫平台);exit code 同 sync。
  - **C 沒有重複、這次檢查過的每個平台的 L 也沒有 → 「沒有重複」、`errSkipCommit`、零寫入**:pull 半邊看到的其他變更留給 `pl sync`(stderr 提一句),
    dedup 不退化成 sync。`--provider` 沒選到、foreign、restricted 的平台是「沒看過」不是「沒重複」:stderr 說它這次沒檢查,結論句改成
    「正本與這次檢查的平台沒有重複」(PR #54 review)。
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

## 3. `capy migrate [來源清單] --from <A> --to <B[:清單]> [--dry-run] [--yes]`(第二個 PR;`internal/cli/migrate.go`、決策 39)

一個 `withCanonical` 裡依序做完,既有的核心函式都直接叫得到(`observeAndDerive`、`canon.Observe` + `absorb`、`pl.Append`、
`planResolve` / `applyMappings` / `reviewLoop`、`planPush` / `applyPlans` / `finishPush`);唯一要抽的是 `pl link --create` 的同名檢查
(`sameNamePlaylists`,兩邊共用)。

- **讀來源**:直接讀 A 的清單,不連結、不記 base(使用者定案:一次性複製;沒有 base 的包袱,之後 `pl sync` 不會因 bootstrap 把 B 重排成 A 的順序)。
- **決定正本 C**(`migrateCanonical`):B 既有且已連著某個 C → 沿用它;B 既有沒連 → 找同名(**B 的名字**,不然 push 會多排 rename 把使用者的清單改名)
  的 C 接管,沒有就建,連著 B 平台別的清單的擋下;B 新建 → 以 A 的名字找同名的 C:沒有就建,連著 B 平台清單的擋下(指路 `--to B:<id>`),
  其餘沿用——README 手動流程做到一半的人(canonical 已連著來源)就在這裡,再建一個同名的會讓 `find` 永遠歧義。
- **既有的 B 先 `observeAndDerive`(只 pull 它)**:C 之後就是 B 的原樣(前綴),push 的兩個前提也靠這一步;pull 半邊撞閾值 → exit 3 指路 sync。
- **A → C 尾端**:`Observe(A)` 拿 cid、`absorb`;C 沒有的(`Redirect(cid)` 比)依 A 的順序 `Append`,同 cid 略過(A 自己的重複也只留一份)。
  A 的曲目都已在既有的 B 裡 → 「都已在,無變更」零寫入(pull 半邊看到的平台變更也留給 `pl sync`,不退化成一次 sync,同 `pl dedup`);新建的 B 則還要把正本既有的推過去。
  push 失敗(平台 403、確認期間變了)時 COMMIT 照走(正本已接上、連結已記),結尾經 `finishPush` 以那個錯收場,exit 1、不講成功;剛建的清單不在第二次 list 裡則報錯並附 `pl link … 接回來` 的命令。
- **resolve 到 B**:`planResolve` + `applyMappings`;新建的 B 還沒有 id 而 `resolve.Needs` 只看有 link 的 (清單, provider),給副本一個佔位 link。
  TTY 且沒對到的 > 0 → 問一次要不要當場 `reviewLoop`(Esc = 整輪不寫入,清單也還沒建);`--yes` 不問。
- **一張表、一次確認**:`syncHeader`,`dir` ∈ `pull` / `migrate`(接在尾端的來源曲目,reason 說推到哪或「沒有對應,這次不推」)/ `push`
  (既有的 B 才算得出來)。`--dry-run` exit 2(`--yes` 也不放行)、非 TTY 沒 `--yes` exit 2、確認畫面說清楚幾首沒對到、正本既有幾首會一起推到新清單。
- **確認之後才建清單**:取消、dry-run、擋下都不會在平台留下沒人連的空清單。建好後用**新的** `newPlatforms` 再 `observeAndDerive` 一次
  (重新 list 才看得到它、讀到空清單、記下 base)。
- **push strict 且只准 add**(`migratePlanPush`):B 有待同步的移除 / 換序 / 改名 → exit 3 先 `pl sync`——「只新增」是 migrate 的承諾,
  待同步的變更交給 sync 而不是順手做掉。`applyPlans` → `finishPush`;新建後 COMMIT 失敗的訊息帶 `pl link` 接回的命令。
- 結尾:搬了幾首、幾首沒對到(指路 `resolve --review` + `pl sync`)、來源沒有連結(要跟著來源就 `pl link` + `pl sync`)。
- 不帶參數 + TTY 四段挑選(來源平台 → 清單 → 目標平台 → 既有的或「建一個跟來源同名的新清單」);非 TTY 要 `--from` / `--to` / `--yes`,
  錯誤訊息一字不變。Apple 不能當目標(只讀);local 只能加進既有檔。頂層 `capy migrate`。
- 測試:新建(來源順序、重複只留一份、dry-run / 非 TTY 不建、只有目標連著、再跑撞同名指路、指到既有「都已在」零寫入、之後 sync 零變更)、
  加進既有(目標原 item 逐位元不變、沿用它連的正本、來源不連結)、既有沒連(正本叫目標的名字、沒有 rename)、沿用連著來源的正本、
  沒對到的跳過再 pin + sync 補上、TTY 當場裁決與取消不建、四段挑選、各種拒絕(同名、缺 flag、只讀目標、空來源、待同步的移除)。
- 可用性卡維護者的 Spotify 真帳號 smoke test(建清單 + 推曲目,同 PR #48);Apple 當目標等 R-8 / T6。

## 4. 產出
- PR (1) `feat/pl-dedup`:§1 + §2,附 ARCHITECTURE v0.10、README、指南 → PR #54 已合併(2026-09-15)。
- PR (2) `feat/migrate`:§3,附 ARCHITECTURE v0.11(附錄 A、決策 39)、README / 指南「跨平台複製清單」改以 migrate 開頭、手動七步留作「背後在做什麼」。
