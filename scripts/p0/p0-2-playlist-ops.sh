#!/usr/bin/env bash
# R-8 / P0-2:Apple Music library playlist 寫入探測(計畫 docs/superpowers/plans/2026-09-22-apple-write.md §5)。
# 會在你的音樂庫建一個拋棄式清單(名稱帶時間戳)、對它做 POST / PATCH / PUT,結尾 DELETE 清單自己清場。
# 只打計畫 §1 表格裡的端點。安全規則:每個變數非空才進 URL;絕不送不帶 ids[…] 的 DELETE …/tracks(會清空整個清單)。
# 前置:capy auth login apple(見 README);token 由 capy debug apple-token 讀 keychain,只進變數、不印。
# 用法:bash scripts/p0/p0-2-playlist-ops.sh   [TERM_QUERY=搜尋詞  CAPY=capy]
set -euo pipefail

CAPY="${CAPY:-capy}"
BASE="${CAPY_APPLE_API_BASE:-https://amp-api.music.apple.com/v1}"
SF="$(${CAPY} config get apple_storefront 2>/dev/null || true)"
SF="${SF:-tw}" # config get 未設時 exit 0 印空行,|| 擋不住
TERM_QUERY="${TERM_QUERY:-五月天}"
DT="$(${CAPY} debug apple-token)"
MUT="$(${CAPY} debug apple-token --user)"
[ -n "${DT}" ] && [ -n "${MUT}" ] || { echo "拿不到 token,先 capy auth login apple" >&2; exit 1; }
H=(-H "Authorization: Bearer ${DT}" -H "Media-User-Token: ${MUT}" -H "Origin: https://music.apple.com")
BODY="$(mktemp)"; trap 'rm -f "${BODY}"' EXIT

# req METHOD PATH [JSON]:印 HTTP 狀態到 stdout、body 存到 $BODY(不含 token)。
req() {
  local m="$1" p="$2" d="${3:-}"
  [ -n "${p}" ] || { echo "空路徑" >&2; exit 1; }
  if [ -n "${d}" ]; then
    curl -sS -o "${BODY}" -w '%{http_code}' -X "${m}" "${H[@]}" -H 'Content-Type: application/json' -d "${d}" "${BASE}${p}"
  else
    curl -sS -o "${BODY}" -w '%{http_code}' -X "${m}" "${H[@]}" "${BASE}${p}"
  fi
}
need() { [ -n "${2:-}" ] && [ "${2}" != "null" ] || { echo "${1} 為空,中止(回應:$(cat "${BODY}" | head -c 300))" >&2; cleanup; exit 1; }; }
entries() { # 讀回 /tracks:每行「列id<TAB>catalogId<TAB>名稱<TAB>type」
  req GET "/me/library/playlists/${PL}/tracks?limit=100" >/dev/null
  jq -r '.data[] | [.id, (.attributes.playParams.catalogId // "-"), .attributes.name, .type] | @tsv' "${BODY}"
}
# tolist <逐行列 id> <期望筆數>:組 PUT body。空、或筆數不符就中止——PUT 是全量取代,送出空陣列 = 清空整份,跟 DELETE 不帶 ids 一樣危險。
tolist() {
  local n; n="$(printf '%s' "$1" | grep -c . || true)"
  [ "${n}" -gt 0 ] && [ "${n}" -eq "$2" ] || { echo "要 PUT 的列數 ${n} 不是期望的 $2,不送(前一步沒生效?)" >&2; exit 1; }
  printf '%s\n' $1 | jq -R . | jq -sc 'map({id: ., type: "library-songs"})'
}
vhash() { req GET "/me/library/playlists/${PL}" >/dev/null; jq -r '.data[0].attributes.playParams.versionHash // "-"' "${BODY}"; }
PL=""
cleanup() {
  if [ "${KEEP:-0}" = "1" ]; then echo "KEEP=1:保留測試清單 ${PL},之後請手動刪"; return; fi
  if [ -n "${PL}" ] && [[ "${PL}" =~ ^p\. ]]; then
    echo "🧹 DELETE /me/library/playlists/${PL} → HTTP $(req DELETE "/me/library/playlists/${PL}")"
  fi
}

echo "① 搜尋 7 首 catalog 歌曲當素材(${SF}:${TERM_QUERY})"
req GET "/catalog/${SF}/search?types=songs&limit=10&term=$(jq -rn --arg s "${TERM_QUERY}" '$s|@uri')" >/dev/null
# 不用 mapfile(macOS bash 3.2);id 無空白
ids=($(jq -r '.results.songs.data[].id' "${BODY}" | awk '!seen[$0]++' | head -n 7))
[ "${#ids[@]}" -ge 7 ] || { echo "只找到 ${#ids[@]} 首,換 TERM_QUERY 再跑" >&2; exit 1; }
echo "   ids: ${ids[*]}"

NAME="capy-r8-$(date +%s)"
echo "② POST /me/library/playlists(不帶曲目,isPublic:false)"
T0=$(date +%s)
code="$(req POST "/me/library/playlists" "{\"attributes\":{\"name\":\"${NAME}\",\"description\":\"capy R-8 探測,可刪\",\"isPublic\":false}}")"
PL="$(jq -r '.data[0].id // empty' "${BODY}")"
echo "   HTTP ${code}  id=${PL}  canEdit=$(jq -r '.data[0].attributes.canEdit' "${BODY}")  versionHash=$(jq -r '.data[0].attributes.playParams.versionHash // "-"' "${BODY}")"
need "清單 id" "${PL}"
trap 'cleanup; rm -f "${BODY}"' EXIT

echo "③ 傳播延遲(判定項 3)"
for i in $(seq 1 60); do
  c="$(req GET "/me/library/playlists/${PL}")"; [ "${c}" = "200" ] && break; sleep 1
done
echo "   GET …/playlists/{id} 200 需時 $(( $(date +%s) - T0 )) s(最後 HTTP ${c})"
for i in $(seq 1 60); do
  req GET "/me/library/playlists?limit=100" >/dev/null
  jq -e --arg id "${PL}" '.data[] | select(.id==$id)' "${BODY}" >/dev/null 2>&1 && break; sleep 1
done
echo "   列表出現需時 $(( $(date +%s) - T0 )) s"

echo "④ append 兩批各 3 首(判定項 4、7);第一批帶 ?representation=resources"
V0="$(vhash)"
code="$(req POST "/me/library/playlists/${PL}/tracks?representation=resources" "{\"data\":[{\"id\":\"${ids[0]}\",\"type\":\"songs\"},{\"id\":\"${ids[1]}\",\"type\":\"songs\"},{\"id\":\"${ids[2]}\",\"type\":\"songs\"}]}")"
echo "   POST 批 A(representation=resources)→ HTTP ${code};回應列 id:$(jq -r '[.data[]?.id] | join(" ")' "${BODY}" 2>/dev/null || echo '(無 body)')"
code="$(req POST "/me/library/playlists/${PL}/tracks" "{\"data\":[{\"id\":\"${ids[3]}\",\"type\":\"songs\"},{\"id\":\"${ids[4]}\",\"type\":\"songs\"},{\"id\":\"${ids[5]}\",\"type\":\"songs\"}]}")"
echo "   POST 批 B → HTTP ${code}"
echo "   POST 後立刻讀 /tracks:$(entries | wc -l | tr -d ' ') 列(期望 6;少於 6 = /tracks 也有延遲,finishPush 重讀 L′ 會拿到舊資料)"
sleep 2
echo "   讀回(列id / catalogId / 名稱):"; entries | sed 's/^/     /'
got="$(entries | cut -f2 | tr '\n' ' ')"
[ "${got}" = "${ids[0]} ${ids[1]} ${ids[2]} ${ids[3]} ${ids[4]} ${ids[5]} " ] && echo "   順序 = A 後 B ✅" || echo "   順序不符 ⚠️  得到:${got}"
echo "   列 id 前綴分佈:$(entries | cut -f1 | cut -d. -f1 | sort | uniq -c | tr '\n' ' ')(a. = 只在清單、i. = 進了曲庫);type:$(entries | cut -f4 | sort | uniq -c | tr '\n' ' ')"
V1="$(vhash)"; echo "   versionHash(判定項 6):add 前 ${V0} → add 後 ${V1} $([ "${V0}" != "${V1}" ] && echo '(有變)' || echo '(沒變)')"

echo "⑤ 重複曲目(判定項 2):再 POST 一次 ids[0]"
code="$(req POST "/me/library/playlists/${PL}/tracks" "{\"data\":[{\"id\":\"${ids[0]}\",\"type\":\"songs\"}]}")"
sleep 2
echo "   HTTP ${code};現在共 $(entries | wc -l | tr -d ' ') 列;catalog ${ids[0]} 的列 id:$(entries | awk -F'\t' -v c="${ids[0]}" '$2==c{print $1}' | tr '\n' ' ')"

echo "⑥ PATCH 只帶 name(判定項 5)"
code="$(req PATCH "/me/library/playlists/${PL}" "{\"attributes\":{\"name\":\"${NAME}-renamed\"}}")"
req GET "/me/library/playlists/${PL}" >/dev/null
echo "   HTTP ${code};name=$(jq -r '.data[0].attributes.name' "${BODY}")  description=$(jq -r '.data[0].attributes.description.standard // "(空)"' "${BODY}")"

echo "⑦ PUT 整批取代:反序、含重複那兩列(判定項 2、8)"
n7="$(entries | wc -l | tr -d ' ')"
rev="$(entries | cut -f1 | tail -r 2>/dev/null || entries | cut -f1 | sed '1!G;h;$!d')"
data="$(tolist "${rev}" "${n7}")"
code="$(req PUT "/me/library/playlists/${PL}/tracks" "{\"data\":${data}}")"
sleep 2
echo "   HTTP ${code};讀回順序(catalogId):$(entries | cut -f2 | tr '\n' ' ')  共 $(entries | wc -l | tr -d ' ') 列"

echo "⑧ PUT 去掉一份重複(判定項 2):按位置拿掉 catalog ${ids[0]} 的最後一列,其餘照原序(兩種列 id 形狀都能判定)"
before="$(entries | wc -l | tr -d ' ')"
uniq_ids="$(entries | awk -F'\t' -v c="${ids[0]}" 'BEGIN{last=0} {rows[NR]=$1; if($2==c) last=NR} END{for(i=1;i<=NR;i++) if(i!=last) print rows[i]}')"
data="$(tolist "${uniq_ids}" "$(( before - 1 ))")"
code="$(req PUT "/me/library/playlists/${PL}/tracks" "{\"data\":${data}}")"
sleep 2
after="$(entries | wc -l | tr -d ' ')"
echo "   HTTP ${code};${before} → ${after} 列(期望少 1;少 2 = 同 id 兩列一起消失,重複無法表達;沒少 = PUT 沒生效)"
echo "   catalog ${ids[0]} 還剩 $(entries | awk -F'\t' -v c="${ids[0]}" '$2==c' | wc -l | tr -d ' ') 列(期望 1)"

echo "⑨ PUT 混型(判定項 1):新曲 ids[6](type songs)插到位置 0,其餘既有列跟在後面"
uniq_ids="$(entries | cut -f1)"
data="$(tolist "${uniq_ids}" "${after}" | jq -c --arg n "${ids[6]}" '[{id: $n, type: "songs"}] + .')"
code="$(req PUT "/me/library/playlists/${PL}/tracks" "{\"data\":${data}}")"
sleep 2
first="$(entries | head -n 1 | cut -f2)"
echo "   HTTP ${code};第一列 catalogId=${first} $([ "${first}" = "${ids[6]}" ] && echo '= 新曲 ✅ 一次 PUT 可行' || echo '≠ 新曲 ⚠️  走兩段式')  共 $(entries | wc -l | tr -d ' ') 列"

echo "⑩ PUT 移除一列(尾端那列拿掉)"
n9="$(entries | wc -l | tr -d ' ')"
keep="$(entries | cut -f1 | sed '$d')"
data="$(tolist "${keep}" "$(( n9 - 1 ))")"
code="$(req PUT "/me/library/playlists/${PL}/tracks" "{\"data\":${data}}")"
sleep 2
echo "   HTTP ${code};共 $(entries | wc -l | tr -d ' ') 列"

echo
echo "把 ②–⑩ 的 HTTP 狀態與觀察貼回計畫 §5、ARCHITECTURE §1.2「Library playlist 寫入」列。"
# bash 收到 SIGINT 結束時仍會跑 EXIT trap,所以 Ctrl-C 要先解除 EXIT trap 才留得住清單。
trap 'trap - EXIT; rm -f "${BODY}"; echo; echo "保留測試清單 ${NAME}-renamed(要看 Music.app 資料庫有沒有多出歌),之後請手動刪"; exit 130' INT
read -r -t 15 -p "15 秒內按 Ctrl-C 可保留測試清單 ${NAME}-renamed(要去 Music.app 看曲目有沒有進資料庫就按);按 Enter、逾時、非互動都會刪掉(非互動要留請設 KEEP=1):" _ || true
trap - INT
