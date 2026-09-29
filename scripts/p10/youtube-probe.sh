#!/usr/bin/env bash
# T0 / P10:YouTube Music(InnerTube)探測(計畫 docs/superpowers/plans/2026-09-29-youtube-music.md §4)。
# 讀端:帳號、清單列表、清單內容、搜尋、單曲、限流、cookie 白名單與大小、google.com 交叉檢查(唯讀)。
# 寫端(WRITE=1):建一個拋棄式清單(名稱帶時間戳),對它做 ADD / REMOVE+ADD / 壞 id / 重複 / 改名 / 搬動,結尾 playlist/delete 自己清場。
# 安全規則:headers 由使用者自己從 DevTools 複製、存成檔案(腳本不碰瀏覽器、不印 cookie、不把 cookie 寫進任何輸出檔);
# 只對名稱以 capy-probe- 開頭、本次建立的清單送寫入;playlist/delete 只刪它。
# 用法:bash scripts/p10/youtube-probe.sh [headers 檔]   環境變數:OUT=輸出目錄 WRITE=1 開寫端 KEEP=1 不刪測試清單
#      PROBE_PLAYLIST=<playlistId> 指定要讀的清單(預設挑最多首的) PROBE_QUERIES='a|b|c' 搜尋詞 RATE=0 跳過限流測試 PROBE_MAX=N 寫入上限
#      MODE=ping 只打一次 account_menu(測 cookie 壽命用);MODE=size 只做白名單 / 大小
set -euo pipefail

HF="${1:-${CAPY_YOUTUBE_HEADERS_FILE:-$HOME/.capy-youtube-headers.txt}}"
[ -r "${HF}" ] || { echo "讀不到 headers 檔 ${HF}:把 music.youtube.com 任一 /browse 請求的 Request Headers 整段貼進去存檔" >&2; exit 1; }
OUT="${OUT:-${TMPDIR:-/tmp}/capy-youtube-probe}"; mkdir -p "${OUT}"
MODE="${MODE:-read}"
BASE="${CAPY_YT_BASE:-https://music.youtube.com/youtubei/v1}" # 測試時指到假伺服器
ORIGIN="https://music.youtube.com"
UA="Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
CV="1.$(date -u +%Y%m%d).01.00"

# 解析貼上的 headers:只取三個,其他一律丟掉;值裡的 \r 去掉。
hdr() { grep -i "^${1}:" "${HF}" | head -n1 | sed -e "s/^[^:]*:[[:space:]]*//" -e 's/\r$//' || true; }
COOKIE="$(hdr cookie)"; AU="$(hdr x-goog-authuser)"; PAGEID="$(hdr x-goog-pageid)"
[ -n "${COOKIE}" ] || { echo "headers 檔裡沒有 cookie: 這一行(要從已登入的 /browse POST 複製)" >&2; exit 1; }
AU="${AU:-0}"
# cookie 裡的某個值(名稱精確比對);__Secure-3PAPISID 沒有就退 SAPISID。
ck() { printf '%s' "${COOKIE}" | tr ';' '\n' | sed 's/^ *//' | awk -F= -v k="$1" '$1==k {sub(/^[^=]*=/, ""); print; exit}'; }
SAPISID="$(ck __Secure-3PAPISID)"; [ -n "${SAPISID}" ] || SAPISID="$(ck SAPISID)"
[ -n "${SAPISID}" ] || { echo "cookie 裡沒有 __Secure-3PAPISID / SAPISID,不是已登入的請求" >&2; exit 1; }
authz() { local ts; ts="$(date +%s)"; printf 'SAPISIDHASH %s_%s' "${ts}" "$(printf '%s %s %s' "${ts}" "${SAPISID}" "${ORIGIN}" | shasum -a 1 | cut -d' ' -f1)"; }
ctx() { # 組 body:context + 參數
  if [ -n "${PAGEID}" ]; then
    jq -nc --arg cv "${CV}" --arg u "${PAGEID}" --argjson b "$1" '{context:{client:{clientName:"WEB_REMIX",clientVersion:$cv,hl:"en",gl:"US"},user:{onBehalfOfUser:$u}}} + $b'
  else
    jq -nc --arg cv "${CV}" --argjson b "$1" '{context:{client:{clientName:"WEB_REMIX",clientVersion:$cv,hl:"en",gl:"US"},user:{}}} + $b'
  fi
}
BODY="${OUT}/last.json"; HDRS="${OUT}/last.headers"
# yt <endpoint> <json 參數> [cookie 覆寫]:印 HTTP 狀態;回應存 $BODY(cookie 永遠不寫進去)。
yt() {
  local ep="$1" args="${2:-{\}}" c="${3:-${COOKIE}}" extra=()
  [ -n "${PAGEID}" ] && extra=(-H "X-Goog-PageId: ${PAGEID}")
  curl -sS -o "${BODY}" -D "${HDRS}" -w '%{http_code}' -X POST \
    -H "Cookie: ${c}" -H "Authorization: $(authz)" -H "X-Goog-AuthUser: ${AU}" -H "X-Origin: ${ORIGIN}" -H "Origin: ${ORIGIN}" \
    -H "Content-Type: application/json" -H "User-Agent: ${UA}" ${extra[@]+"${extra[@]}"} \
    --data-binary "$(ctx "${args}")" "${BASE}/${ep}?alt=json&prettyPrint=false"
}
keep() { cp "${BODY}" "${OUT}/$1.json"; } # 存一份給 fixture 用(裡面沒有 cookie;帳號名 / 頭像之後做 fixture 時再去識別化)
ACC='.actions[0].openPopupAction.popup.multiPageMenuRenderer.header.activeAccountHeaderRenderer'
acct() { jq -r "${ACC} | [(.accountName.runs[0].text // \"-\"), (.channelHandle.runs[0].text // \"-\")] | @tsv" "${BODY}" 2>/dev/null || echo "-	-"; }
logged_in() { jq -e "${ACC}.accountName" "${BODY}" >/dev/null 2>&1; }
ok() { jq -r '.status // "-"' "${BODY}"; }

if [ "${MODE}" = "ping" ]; then
  code="$(yt account/account_menu '{}')"; printf '%s ping account_menu → HTTP %s ' "$(date -u +%FT%TZ)" "${code}"
  if logged_in; then echo "登入中($(acct | cut -f2))"; else echo "未登入 / 失效"; fi
  exit 0
fi

echo "① account_menu"
code="$(yt account/account_menu '{}')"; keep 01-account_menu; echo "   HTTP ${code};帳號 / handle:$(acct)"
logged_in || { echo "   ✗ 回應裡沒有帳號:cookie 失效、authuser 抄錯、或 hl 影響;回應開頭:$(head -c 300 "${BODY}")"; exit 1; }
echo "   回應裡的 UC… browseId(可能是 channel id):$(jq -r '[.. | objects | .browseId? // empty | select(startswith("UC"))] | unique | join(", ")' "${BODY}")"
echo "   有沒有 email 字樣:$(grep -c '@' "${BODY}" || true) 個 @(handle 也算);actions 數:$(jq '.actions | length' "${BODY}")"
if [ "${AU}" != "0" ]; then echo "   (x-goog-authuser=${AU})"; fi
alt="$(( AU + 1 ))"; AU_SAVE="${AU}"; AU="${alt}"; code="$(yt account/account_menu '{}')"; echo "   authuser=${alt} 試打 → HTTP ${code}、$( logged_in && echo "另一個帳號:$(acct | cut -f2)" || echo "沒有帳號(單帳號瀏覽器)")"; AU="${AU_SAVE}"

echo "② cookie 大小與白名單(對 Windows 2560 / macOS 約 3000 bytes)"
echo "   整段 cookie:$(printf '%s' "${COOKIE}" | wc -c | tr -d ' ') bytes;cookie 個數:$(printf '%s' "${COOKIE}" | tr ';' '\n' | grep -c . )"
subset() { printf '%s' "${COOKIE}" | tr ';' '\n' | sed 's/^ *//' | awk -F= -v keep="$1" 'BEGIN{n=split(keep,a," ");for(i=1;i<=n;i++)w[a[i]]=1} $1 in w {print}' | paste -sd ';' - | sed 's/;/; /g'; }
for set in \
  "S1:__Secure-3PSID __Secure-3PAPISID" \
  "S2:__Secure-3PSID __Secure-3PAPISID __Secure-3PSIDTS __Secure-3PSIDCC" \
  "S3:SID HSID SSID APISID SAPISID __Secure-1PSID __Secure-3PSID __Secure-1PAPISID __Secure-3PAPISID" \
  "S4:SID HSID SSID APISID SAPISID __Secure-1PSID __Secure-3PSID __Secure-1PAPISID __Secure-3PAPISID LOGIN_INFO" \
  "S5:SID HSID SSID APISID SAPISID __Secure-1PSID __Secure-3PSID __Secure-1PAPISID __Secure-3PAPISID __Secure-1PSIDTS __Secure-3PSIDTS SIDCC __Secure-1PSIDCC __Secure-3PSIDCC" ; do
  name="${set%%:*}"; names="${set#*:}"; c="$(subset "${names}")"
  code="$(yt account/account_menu '{}' "${c}")"
  printf '   %s %5s bytes → HTTP %s %s\n' "${name}" "$(printf '%s' "${c}" | wc -c | tr -d ' ')" "${code}" "$( logged_in && echo '登入 ✓' || echo '未登入 ✗')"
done
[ "${MODE}" = "size" ] && exit 0

echo "③ FEmusic_liked_playlists"
code="$(yt browse '{"browseId":"FEmusic_liked_playlists"}')"; keep 03-library_playlists; echo "   HTTP ${code}"
jq -r '[.. | objects | select(has("musicTwoRowItemRenderer")) | .musicTwoRowItemRenderer | [(.navigationEndpoint.browseEndpoint.browseId // "-"), (.title.runs[0].text // "-"), ([.subtitle.runs[]?.text] | join(""))] | @tsv] | .[]' "${BODY}" > "${OUT}/03-playlists.tsv"
echo "   grid 項目 $(wc -l < "${OUT}/03-playlists.tsv" | tr -d ' ') 個;前 8 個(browseId / 名稱 / 副標):"; head -n 8 "${OUT}/03-playlists.tsv" | sed 's/^/     /'
echo "   有 LM 嗎:$(grep -c '^VLLM' "${OUT}/03-playlists.tsv" || true);continuation:$(jq '[.. | objects | select(has("continuationItemRenderer") or has("continuations"))] | length' "${BODY}") 處;第 0 格的 renderer:$(jq -r '[.. | objects | select(has("gridRenderer")) | .gridRenderer.items[0] | keys[]] | first // "-"' "${BODY}")"
echo "   grid 每項有哪些鍵(owned 訊號?):$(jq -r '[.. | objects | select(has("musicTwoRowItemRenderer")) | .musicTwoRowItemRenderer | keys[]] | unique | join(",")' "${BODY}")"

# 挑一份清單:PROBE_PLAYLIST 或副標裡曲數最多的(排除 LM 與非 VL 的)
pick() { awk -F'\t' '$1 ~ /^VL/ && $1 != "VLLM" { n=0; if (match($3, /[0-9,]+ (songs|tracks|首)/)) { n=substr($3, RSTART, RLENGTH); gsub(/[^0-9]/, "", n) } print n "\t" substr($1, 3) "\t" $2 }' "${OUT}/03-playlists.tsv" | sort -rn | head -n1; }
if [ -n "${PROBE_PLAYLIST:-}" ]; then PL_ID="${PROBE_PLAYLIST}"; PL_NAME="(指定)"; PL_N="?"; else line="$(pick)"; PL_N="$(printf '%s' "${line}" | cut -f1)"; PL_ID="$(printf '%s' "${line}" | cut -f2)"; PL_NAME="$(printf '%s' "${line}" | cut -f3)"; fi
[ -n "${PL_ID}" ] || { echo "   找不到可讀的清單,之後的步驟用 PROBE_PLAYLIST 指定" >&2; exit 1; }

# rows <playlistId> [max continuations]:跟著 continuation 讀整份,輸出 TSV:videoId setVideoId title videoType greyed
ROWS='[.. | objects | select(has("musicResponsiveListItemRenderer")) | .musicResponsiveListItemRenderer | [(.playlistItemData.videoId // "-"), (.playlistItemData.playlistSetVideoId // "-"), (.flexColumns[0].musicResponsiveListItemFlexColumnRenderer.text.runs[0].text // "-"), (([.. | objects | .musicVideoType? // empty] | first) // "-"), (if (.musicItemRendererDisplayPolicy // "") == "MUSIC_ITEM_RENDERER_DISPLAY_POLICY_GREY_OUT" then "grey" else "-" end)] | @tsv] | .[]'
TOK='[.. | objects | .continuationItemRenderer? // empty | .continuationEndpoint.continuationCommand.token] | first // ([.. | objects | .continuations? // empty | .[0].nextContinuationData.continuation] | first) // ""'
rows() {
  local id="$1" max="${2:-50}" i=0 tok
  code="$(yt browse "$(jq -nc --arg b "VL${id}" '{browseId:$b}')")"; [ "${code}" = "200" ] || { echo "   browse VL${id} → HTTP ${code}" >&2; return 1; }
  cp "${BODY}" "${OUT}/rows-first.json"
  jq -r "${ROWS}" "${BODY}"
  tok="$(jq -r "${TOK}" "${BODY}")"
  while [ -n "${tok}" ] && [ "${i}" -lt "${max}" ]; do
    i=$((i+1)); code="$(yt browse "$(jq -nc --arg t "${tok}" '{continuation:$t}')")"; [ "${code}" = "200" ] || { echo "   continuation ${i} → HTTP ${code}" >&2; return 1; }
    jq -r "${ROWS}" "${BODY}"; tok="$(jq -r "${TOK}" "${BODY}")"
  done
  CONT_N="${i}"
}
echo "④ VL${PL_ID}(${PL_NAME},列表說 ${PL_N} 首)"
rows "${PL_ID}" > "${OUT}/04-rows.tsv"; cp "${OUT}/rows-first.json" "${OUT}/04-playlist_first_page.json"
echo "   讀到 $(wc -l < "${OUT}/04-rows.tsv" | tr -d ' ') 列、跟了 ${CONT_N:-0} 次 continuation;沒 videoId 的列:$(awk -F'\t' '$1=="-"' "${OUT}/04-rows.tsv" | wc -l | tr -d ' ');grey:$(awk -F'\t' '$5=="grey"' "${OUT}/04-rows.tsv" | wc -l | tr -d ' ')"
echo "   videoType 分布:$(cut -f4 "${OUT}/04-rows.tsv" | sort | uniq -c | awk '{printf "%s×%s ", $2, $1}')"
echo "   同一 videoId 出現多次:$(cut -f1 "${OUT}/04-rows.tsv" | grep -v '^-$' | sort | uniq -d | wc -l | tr -d ' ') 首;setVideoId 唯一數 / 列數:$(cut -f2 "${OUT}/04-rows.tsv" | sort -u | wc -l | tr -d ' ') / $(wc -l < "${OUT}/04-rows.tsv" | tr -d ' ')"
echo "   header 型別:$(jq -r '[.. | objects | keys[] | select(test("Header"))] | unique | join(",")' "${OUT}/04-playlist_first_page.json")"
echo "   前 3 列:"; head -n 3 "${OUT}/04-rows.tsv" | sed 's/^/     /'

echo "⑥ search(歌曲 filter)"
IFS='|' read -r -a QS <<< "${PROBE_QUERIES:-五月天 派對動物|米津玄師 Lemon|Coldplay Yellow}"
for q in "${QS[@]}"; do
  code="$(yt search "$(jq -nc --arg q "${q}" '{query:$q, params:"EgWKAQIIAWoMEA4QChADEAQQCRAF"}')")"; keep "06-search-$(printf '%s' "${q}" | tr -c 'A-Za-z0-9' '_' | cut -c1-30)"
  echo "   「${q}」→ HTTP ${code};shelf 標題:$(jq -r '[.. | objects | .musicShelfRenderer? // empty | .title.runs[0].text] | join(",")' "${BODY}");前 3:"
  jq -r '[.. | objects | select(has("musicResponsiveListItemRenderer")) | .musicResponsiveListItemRenderer | [(.playlistItemData.videoId // (.overlay.musicItemThumbnailOverlayRenderer.content.musicPlayButtonRenderer.playNavigationEndpoint.watchEndpoint.videoId // "-")), (.flexColumns[0].musicResponsiveListItemFlexColumnRenderer.text.runs[0].text // "-"), ([.flexColumns[1].musicResponsiveListItemFlexColumnRenderer.text.runs[]?.text] | join("")), (([.badges[]?.musicInlineBadgeRenderer.accessibilityData.accessibilityData.label] | join(",")))] | @tsv] | .[0:3][]' "${BODY}" | sed 's/^/     /'
done

echo "⑧ next(單曲 metadata)"
VID="$(awk -F'\t' '$1!="-" {print $1; exit}' "${OUT}/04-rows.tsv")"
code="$(yt next "$(jq -nc --arg v "${VID}" '{videoId:$v, isAudioOnly:true, tunerSettingValue:"AUTOMIX_SETTING_NORMAL", enablePersistentPlaylistPanel:true}')")"; keep 08-next
echo "   HTTP ${code};第一項:$(jq -r '[.. | objects | .playlistPanelVideoRenderer? // empty] | first | [(.videoId // "-"), (.title.runs[0].text // "-"), ([.longBylineText.runs[]?.text] | join("")), (.lengthText.runs[0].text // "-")] | @tsv' "${BODY}")"

echo "⑪ 同一段 cookie 對 google.com(唯讀)"
printf '   myaccount.google.com → HTTP %s\n' "$(curl -sS -o /dev/null -w '%{http_code} → %{redirect_url}' -H "Cookie: ${COOKIE}" -H "User-Agent: ${UA}" https://myaccount.google.com/)"
printf '   drive/v3/about(SAPISIDHASH,origin music)→ HTTP %s\n' "$(curl -sS -o "${OUT}/11-drive.json" -w '%{http_code}' -H "Cookie: ${COOKIE}" -H "Authorization: $(authz)" -H "X-Origin: ${ORIGIN}" -H "User-Agent: ${UA}" 'https://www.googleapis.com/drive/v3/about?fields=user')"
printf '   www.youtube.com/youtubei/v1/account/account_menu(同 cookie、origin 改 www)→ HTTP %s\n' "$(curl -sS -o /dev/null -w '%{http_code}' -X POST -H "Cookie: ${COOKIE}" -H "X-Goog-AuthUser: ${AU}" -H "Content-Type: application/json" -H "User-Agent: ${UA}" --data-binary "$(ctx '{}' | sed 's/WEB_REMIX/WEB/')" 'https://www.youtube.com/youtubei/v1/account/account_menu?alt=json&prettyPrint=false')"

if [ "${RATE:-1}" = "1" ]; then
  echo "⑨ 限流:連打 60 次 search"
  codes=""; ra=""
  for i in $(seq 1 60); do
    c="$(yt search "$(jq -nc --arg q "${QS[$((i % ${#QS[@]}))]} ${i}" '{query:$q, params:"EgWKAQIIAWoMEA4QChADEAQQCRAF"}')")"; codes="${codes} ${c}"
    if [ "${c}" != "200" ]; then ra="$(grep -i '^retry-after' "${HDRS}" || echo "(沒有 Retry-After)")"; echo "   第 ${i} 次 → HTTP ${c} ${ra}"; break; fi
  done
  echo "   結果:$(printf '%s' "${codes}" | tr ' ' '\n' | grep -c 200) 次 200 / $(printf '%s' "${codes}" | wc -w | tr -d ' ') 次"
fi

[ "${WRITE:-0}" = "1" ] || { echo "(讀端完成;WRITE=1 才做寫端 ⑤ ⑦ ⑩)"; exit 0; }

########## 寫端 ##########
PL=""
edit() { # edit <actions json 陣列>:一個 browse/edit_playlist 請求
  [ -n "${PL}" ] || { echo "PL 為空" >&2; exit 1; }
  yt browse/edit_playlist "$(jq -nc --arg p "${PL}" --argjson a "$1" '{playlistId:$p, actions:$a}')"
}
adds() { printf '%s\n' "$@" | jq -R . | jq -sc 'map({action:"ACTION_ADD_VIDEO", addedVideoId:., dedupeOption:"DEDUPE_OPTION_SKIP"})'; }
removes_all() { rows "${PL}" | awk -F'\t' '$1!="-"' | jq -R 'split("\t") | {action:"ACTION_REMOVE_VIDEO", removedVideoId:.[0], setVideoId:.[1]}' | jq -sc .; }
count() { rows "${PL}" | wc -l | tr -d ' '; }
cleanup() {
  [ -n "${PL}" ] || return 0
  if [ "${KEEP:-0}" = "1" ]; then echo "KEEP=1:保留測試清單 ${PL},請手動刪"; return 0; fi
  echo "⑩ playlist/delete ${PL} → HTTP $(yt playlist/delete "$(jq -nc --arg p "${PL}" '{playlistId:$p}')") $(ok)"
  for i in 1 2 3 4 5 6; do sleep 2; yt browse '{"browseId":"FEmusic_liked_playlists"}' >/dev/null; jq -e --arg b "VL${PL}" '[.. | objects | .browseId? // empty] | index($b) == null' "${BODY}" >/dev/null && { echo "   列表 $((i*2)) s 後已消失"; return 0; }; done
  echo "   ⚠ 12 s 後列表仍有 ${PL},請手動確認"
}
trap cleanup EXIT

MAT=($(awk -F'\t' '$1!="-" {print $1}' "${OUT}/04-rows.tsv" | awk '!seen[$0]++'))
[ "${#MAT[@]}" -ge 3 ] || { echo "素材不足(清單 ${PL_ID} 只有 ${#MAT[@]} 首可用)" >&2; exit 1; }
MAXN="${PROBE_MAX:-${#MAT[@]}}"; [ "${MAXN}" -le "${#MAT[@]}" ] || MAXN="${#MAT[@]}"
echo "⑤ 寫端(素材 ${#MAT[@]} 首,寫入上限 ${MAXN})"
NAME="capy-probe-$(date +%s)"
T0=$(date +%s); code="$(yt playlist/create "$(jq -nc --arg t "${NAME}" '{title:$t, privacyStatus:"PRIVATE"}')")"; keep 05a-create
PL="$(jq -r '.playlistId // empty' "${BODY}")"; echo "   5a playlist/create → HTTP ${code} $(ok) id=${PL:-?}"
[ -n "${PL}" ] || { echo "   建不出清單,回應:$(head -c 300 "${BODY}")"; PL=""; exit 1; }
for i in $(seq 1 30); do yt browse '{"browseId":"FEmusic_liked_playlists"}' >/dev/null; jq -e --arg b "VL${PL}" '[.. | objects | .browseId? // empty] | index($b) != null' "${BODY}" >/dev/null && { echo "   列表 $(( $(date +%s) - T0 )) s 後出現"; break; }; sleep 1; done

n=100; [ "${n}" -le "${MAXN}" ] || n="${MAXN}"
while :; do
  code="$(edit "$(adds "${MAT[@]:0:${n}}")")"; keep "05b-add-${n}"
  echo "   5b ADD ${n} 首一個請求 → HTTP ${code} $(ok);edit 結果 $(jq '.playlistEditResults | length' "${BODY}") 筆;讀回 $(count) 列"
  [ "${code}" = "200" ] && [ "$(ok)" = "STATUS_SUCCEEDED" ] || break
  code="$(edit "$(removes_all)")"; echo "      REMOVE 全部一個請求 → HTTP ${code} $(ok);讀回 $(count) 列"
  [ "${n}" -ge "${MAXN}" ] && break
  n=$((n*2)); [ "${n}" -le "${MAXN}" ] || n="${MAXN}"
done

m=50; [ "${m}" -le "${MAXN}" ] || m="${MAXN}"
code="$(edit "$(adds "${MAT[@]:0:${m}}")")"; echo "   5c 先放 ${m} 首 → HTTP ${code} $(ok)"
before="$(rows "${PL}" | cut -f1 | tr '\n' ' ')"
rev=($(printf '%s\n' "${MAT[@]:0:${m}}" | tail -r))
code="$(edit "$(jq -nc --argjson r "$(removes_all)" --argjson a "$(adds "${rev[@]}")" '$r + $a')")"; keep 05c-replace
after="$(rows "${PL}" | cut -f1 | tr '\n' ' ')"
if [ "${after}" = "$(printf '%s ' "${rev[@]}")" ]; then order="順序 = 反序 ✓"; else order="順序不是反序 ✗(before: ${before:0:60}… after: ${after:0:60}…)"; fi
echo "      REMOVE ${m} + ADD ${m}(反序)同一請求 → HTTP ${code} $(ok);讀回 $(count) 列;${order}"

code="$(edit "$(jq -nc --arg a "${MAT[1]}" --arg b "${MAT[2]}" '[{action:"ACTION_ADD_VIDEO", addedVideoId:$a, dedupeOption:"DEDUPE_OPTION_SKIP"},{action:"ACTION_ADD_VIDEO", addedVideoId:"zzzzzzzzzzz", dedupeOption:"DEDUPE_OPTION_SKIP"},{action:"ACTION_ADD_VIDEO", addedVideoId:$b, dedupeOption:"DEDUPE_OPTION_SKIP"}]')")"; keep 05d-bad_id
echo "   5d 好 / 壞 / 好 三個 ADD 一個請求 → HTTP ${code} $(ok);讀回 $(count) 列(原子 = 還是 ${m};部分套用 = ${m}+2)"

c0="$(count)"
code="$(edit "$(jq -nc --arg a "${MAT[0]}" '[{action:"ACTION_ADD_VIDEO", addedVideoId:$a}]')")"; echo "   5e 再加已在清單裡的一首、不帶 dedupeOption → HTTP ${code} $(ok);列數 ${c0} → $(count)"
code="$(edit "$(jq -nc --arg a "${MAT[0]}" '[{action:"ACTION_ADD_VIDEO", addedVideoId:$a, dedupeOption:"DEDUPE_OPTION_SKIP"}]')")"; echo "      帶 DEDUPE_OPTION_SKIP → HTTP ${code} $(ok);列數 → $(count)"

code="$(edit "$(jq -nc --arg t "${NAME}-renamed" '[{action:"ACTION_SET_PLAYLIST_NAME", playlistName:$t}]')")"; keep 05f-rename
rows "${PL}" >/dev/null; echo "   5f 改名 → HTTP ${code} $(ok);讀回標題:$(jq -r '[.. | objects | .title? // empty | .runs[0]?.text // empty] | first // "-"' "${OUT}/rows-first.json")"

first="$(rows "${PL}" | head -n1)"; third="$(rows "${PL}" | sed -n 3p)"
code="$(edit "$(jq -nc --arg s "$(printf '%s' "${first}" | cut -f2)" --arg t "$(printf '%s' "${third}" | cut -f2)" '[{action:"ACTION_MOVE_VIDEO_BEFORE", setVideoId:$s, movedSetVideoIdSuccessor:$t}]')")"
echo "   5g 第 1 列搬到第 3 列前 → HTTP ${code} $(ok);現在前 3 列:$(rows "${PL}" | head -n3 | cut -f1 | tr '\n' ' ')"

echo "⑦ 上傳的歌"
code="$(yt browse '{"browseId":"FEmusic_library_privately_owned_tracks"}')"; keep 07-uploads
UP="$(jq -r "${ROWS}" "${BODY}" | awk -F'\t' '$1!="-" {print $1; exit}')"
if [ -n "${UP}" ]; then code="$(edit "$(adds "${UP}")")"; echo "   有上傳的歌 ${UP};ADD 進測試清單 → HTTP ${code} $(ok);列數 → $(count)"; else echo "   HTTP ${code};這個帳號沒有上傳的歌,略過"; fi
echo "(寫端完成;結尾自動 playlist/delete)"
