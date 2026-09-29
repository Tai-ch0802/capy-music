#!/usr/bin/env bash
# T0 / P10:YouTube Music(InnerTube)探測(計畫 docs/superpowers/plans/2026-09-29-youtube-music.md §4)。
# 讀端:帳號、清單列表、清單內容、搜尋、單曲、限流、cookie 白名單與大小、google.com 交叉檢查(唯讀)。
# 寫端(WRITE=1):建一個拋棄式清單(名稱帶時間戳),對它做 ADD / REMOVE+ADD / 壞 id / 重複 / 改名 / 搬動,結尾 playlist/delete 自己清場。
# 安全規則:headers 由使用者自己從 DevTools 複製、存成檔案(腳本不碰瀏覽器、不印 cookie、不把 cookie 寫進任何輸出檔);
# 只對名稱以 capy-probe- 開頭、本次建立的清單送寫入;playlist/delete 只刪它。
# 用法:bash scripts/p10/youtube-probe.sh [headers 檔]   環境變數:OUT=輸出目錄 WRITE=1 開寫端 KEEP=1 不刪測試清單
#      PROBE_PLAYLIST=<playlistId> 指定要讀的清單(預設挑最多首的) PROBE_QUERIES='a|b|c' 搜尋詞 RATE=0 跳過限流測試 PROBE_MAX=N 寫入上限
#      MODE=ping 只打一次 account_menu(測 cookie 壽命用);MODE=size 只做白名單 / 大小;MODE=cap 只測單請求 ADD 的上限與讀回延遲(CAP_SIZES='466 500 640')
set -euo pipefail

HF="${1:-${CAPY_YOUTUBE_HEADERS_FILE:-$HOME/.capy-youtube-headers.txt}}"
[ -r "${HF}" ] || { echo "讀不到 headers 檔 ${HF}:把 music.youtube.com 任一 /browse 請求的 Request Headers 整段貼進去存檔" >&2; exit 1; }
OUT="${OUT:-${TMPDIR:-/tmp}/capy-youtube-probe}"; mkdir -p "${OUT}"
MODE="${MODE:-read}"
BASE="${CAPY_YT_BASE:-https://music.youtube.com/youtubei/v1}" # 測試時指到假伺服器
ORIGIN="https://music.youtube.com"
UA="Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
CV="1.$(date -u +%Y%m%d).01.00"

# 解析貼上的 headers:接受兩種格式——DevTools 的 Request Headers 純文字(name: value 一行一個)與「Copy as cURL」
# (-H 'name: value' \ 與 -b '<cookie>')。只取三個 header,其他一律丟掉;值裡的 \r 去掉。
NORM_SED="${OUT}/norm.sed"
cat > "${NORM_SED}" <<'SED'
s/\r$//
s/^[[:space:]]*-H[[:space:]]+['"]//
s/^[[:space:]]*(-b|--cookie)[[:space:]]+['"]/cookie: /
s/['"][[:space:]]*\\?[[:space:]]*$//
SED
norm() { sed -E -f "${NORM_SED}" "${HF}"; }
hdr() { norm | grep -i "^${1}:" | head -n1 | sed -e "s/^[^:]*:[[:space:]]*//" || true; }
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
  curl -sS -m 900 -o "${BODY}" -D "${HDRS}" -w '%{http_code}' -X POST \
    -H "Cookie: ${c}" -H "Authorization: $(authz)" -H "X-Goog-AuthUser: ${AU}" -H "X-Origin: ${ORIGIN}" -H "Origin: ${ORIGIN}" \
    -H "Content-Type: application/json" -H "User-Agent: ${UA}" ${extra[@]+"${extra[@]}"} \
    --data-binary "$(ctx "${args}")" "${BASE}/${ep}?alt=json&prettyPrint=false"
}
keep() { cp "${BODY}" "${OUT}/$1.json"; } # 存一份給 fixture 用(裡面沒有 cookie;帳號名 / 頭像之後做 fixture 時再去識別化)
ACC='.actions[0].openPopupAction.popup.multiPageMenuRenderer.header.activeAccountHeaderRenderer'
acct() { jq -r "${ACC} | [(.accountName.runs[0].text // \"-\"), (.channelHandle.runs[0].text // \"-\")] | @tsv" "${BODY}" 2>/dev/null || echo "-	-"; }
logged_in() { jq -e "${ACC}.accountName" "${BODY}" >/dev/null 2>&1; }
ok() { jq -r '.status // "-"' "${BODY}"; }

if [ "${MODE}" = "grid" ]; then # 唯讀:清單列表的 continuation(舊式 nextContinuationData → ctoken / continuation / type=next 查詢參數)
  code="$(yt browse '{"browseId":"FEmusic_liked_playlists"}')"; n=0; tok="$(jq -r '[.. | objects | .continuations? // empty | .[0].nextContinuationData.continuation] | first // ""' "${BODY}")"
  echo "第一頁 HTTP ${code};grid 項目 $(jq '[.. | objects | select(has("musicTwoRowItemRenderer"))] | length' "${BODY}");continuation token 長度 ${#tok}"
  while [ -n "${tok}" ] && [ "${n}" -lt 5 ]; do
    n=$((n+1)); code="$(curl -sS -o "${BODY}" -w '%{http_code}' -X POST -H "Cookie: ${COOKIE}" -H "Authorization: $(authz)" -H "X-Goog-AuthUser: ${AU}" -H "X-Origin: ${ORIGIN}" -H "Origin: ${ORIGIN}" -H "Content-Type: application/json" -H "User-Agent: ${UA}" --data-binary "$(ctx '{}')" "${BASE}/browse?alt=json&prettyPrint=false&ctoken=${tok}&continuation=${tok}&type=next")"
    cp "${BODY}" "${OUT}/grid-cont-${n}.json"
    echo "續頁 ${n} HTTP ${code};頂層鍵:$(jq -r 'keys | join(",")' "${BODY}");continuationContents 的鍵:$(jq -r '.continuationContents // {} | keys | join(",")' "${BODY}");項目 $(jq '[.. | objects | select(has("musicTwoRowItemRenderer"))] | length' "${BODY}")"
    tok="$(jq -r '[.. | objects | .continuations? // empty | .[0].nextContinuationData.continuation] | first // ""' "${BODY}")"
  done
  exit 0
fi
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
if [ "${MODE}" != "cap" ]; then
alt="$(( AU + 1 ))"; AU_SAVE="${AU}"; AU="${alt}"; code="$(yt account/account_menu '{}')"; echo "   authuser=${alt} 試打 → HTTP ${code}、$( logged_in && echo "另一個帳號:$(acct | cut -f2)" || echo "沒有帳號(單帳號瀏覽器)")"; AU="${AU_SAVE}"

echo "② cookie 大小與白名單(對 Windows 2560 / macOS 約 3000 bytes)"
echo "   整段 cookie:$(printf '%s' "${COOKIE}" | wc -c | tr -d ' ') bytes;cookie 個數:$(printf '%s' "${COOKIE}" | tr ';' '\n' | grep -c . )"
subset() { printf '%s' "${COOKIE}" | tr ';' '\n' | sed 's/^ *//' | awk -F= -v keep="$1" 'BEGIN{n=split(keep,a," ");for(i=1;i<=n;i++)w[a[i]]=1} $1 in w {print}' | paste -sd ';' - | sed 's/;/; /g'; }
for set in \
  "S1:__Secure-3PSID __Secure-3PAPISID" \
  "S2:__Secure-3PSID __Secure-3PAPISID __Secure-3PSIDTS __Secure-3PSIDCC" \
  "S2a:__Secure-3PSID __Secure-3PAPISID __Secure-3PSIDTS" \
  "S2b:__Secure-3PSID __Secure-3PAPISID __Secure-3PSIDCC" \
  "S2c:__Secure-1PSID __Secure-1PAPISID __Secure-1PSIDTS __Secure-1PSIDCC" \
  "S3:SID HSID SSID APISID SAPISID __Secure-1PSID __Secure-3PSID __Secure-1PAPISID __Secure-3PAPISID" \
  "S4:SID HSID SSID APISID SAPISID __Secure-1PSID __Secure-3PSID __Secure-1PAPISID __Secure-3PAPISID LOGIN_INFO" \
  "S5:SID HSID SSID APISID SAPISID __Secure-1PSID __Secure-3PSID __Secure-1PAPISID __Secure-3PAPISID __Secure-1PSIDTS __Secure-3PSIDTS SIDCC __Secure-1PSIDCC __Secure-3PSIDCC" ; do
  name="${set%%:*}"; names="${set#*:}"; c="$(subset "${names}")"
  code="$(yt account/account_menu '{}' "${c}")"
  printf '   %s %5s bytes → HTTP %s %s\n' "${name}" "$(printf '%s' "${c}" | wc -c | tr -d ' ')" "${code}" "$( logged_in && echo '登入 ✓' || echo '未登入 ✗')"
done
[ "${MODE}" = "size" ] && exit 0
fi # MODE != cap

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
    cp "${BODY}" "${OUT}/rows-cont-${i}.json" # 給 fixture 用(去識別化後)
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

if [ "${MODE}" != "cap" ]; then
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
printf '   www.youtube.com/youtubei/v1/account/account_menu(同 cookie、origin 改 www)→ HTTP %s\n' "$(curl -sS -o /dev/null -w '%{http_code}' -X POST -H "Cookie: ${COOKIE}" -H "X-Goog-AuthUser: ${AU}" -H "Content-Type: application/json" -H "User-Agent: ${UA}" --data-binary "$(ctx '{}' | sed -e 's/WEB_REMIX/WEB/' -e 's/"1\.\([0-9]*\)\.01\.00"/"2.\1.00.00"/')" 'https://www.youtube.com/youtubei/v1/account/account_menu?alt=json&prettyPrint=false')"

if [ "${RATE:-1}" = "1" ]; then
  echo "⑨ 限流:連打 60 次 search"
  codes=""; ra=""
  for i in $(seq 1 60); do
    c="$(yt search "$(jq -nc --arg q "${QS[$((i % ${#QS[@]}))]} ${i}" '{query:$q, params:"EgWKAQIIAWoMEA4QChADEAQQCRAF"}')")"; codes="${codes} ${c}"
    if [ "${c}" != "200" ]; then ra="$(grep -i '^retry-after' "${HDRS}" || echo "(沒有 Retry-After)")"; echo "   第 ${i} 次 → HTTP ${c} ${ra}"; break; fi
  done
  echo "   結果:$(printf '%s' "${codes}" | tr ' ' '\n' | grep -c 200) 次 200 / $(printf '%s' "${codes}" | wc -w | tr -d ' ') 次"
fi

fi # MODE != cap
[ "${WRITE:-0}" = "1" ] || [ "${MODE}" = "cap" ] || [ "${MODE}" = "replace" ] || [ "${MODE}" = "big" ] || { echo "(讀端完成;WRITE=1 才做寫端 ⑤ ⑦ ⑩)"; exit 0; }

########## 寫端 ##########
PL=""
edit() { # edit <actions json 陣列>:一個 browse/edit_playlist 請求
  [ -n "${PL}" ] || { echo "PL 為空" >&2; exit 1; }
  yt browse/edit_playlist "$(jq -nc --arg p "${PL}" --argjson a "$1" '{playlistId:$p, actions:$a}')"
}
adds() { printf '%s\n' "$@" | jq -R . | jq -sc 'map({action:"ACTION_ADD_VIDEO", addedVideoId:., dedupeOption:"DEDUPE_OPTION_SKIP"})'; }
removes_all() { rows "${PL}" | awk -F'\t' '$1!="-"' | jq -R 'split("\t") | {action:"ACTION_REMOVE_VIDEO", removedVideoId:.[0], setVideoId:.[1]}' | jq -sc .; }
count() { rows "${PL}" | awk -F'\t' '$1!="-"' | wc -l | tr -d ' '; }
# count_wait <期望列數>:讀回可能延遲(2026-09-29 實測 ADD 466 首後立刻讀是 0 列),每 3 s 重讀、最多 60 s;印「N 列(等了 k s)」
count_wait() {
  local want="$1" i=0 n
  while :; do n="$(count)"; if [ "${n}" = "${want}" ] || [ "${i}" -ge 20 ]; then break; fi; i=$((i+1)); sleep 3; done
  if [ "${n}" = "${want}" ]; then printf '%s 列(等了 %d s)' "${n}" "$((i*3))"; else printf '%s 列(%d s 後仍不是 %s)' "${n}" "$((i*3))" "${want}"; fi
}
# grid_count:清單列表副標裡這份清單的曲數(另一個資料來源,跟讀回的列數對照)
grid_count() { yt browse '{"browseId":"FEmusic_liked_playlists"}' >/dev/null; jq -r --arg b "VL${PL}" '[.. | objects | select(has("musicTwoRowItemRenderer")) | .musicTwoRowItemRenderer | select(.navigationEndpoint.browseEndpoint.browseId == $b) | [.subtitle.runs[]?.text] | join("")] | first // "-"' "${BODY}"; }
vids() { rows "${PL}" | awk -F'\t' '$1!="-" {print $1}' | tr '\n' ' '; }
cleanup() {
  [ -n "${PL}" ] || return 0
  if [ "${KEEP:-0}" = "1" ]; then echo "KEEP=1:保留測試清單 ${PL},請手動刪"; return 0; fi
  echo "⑩ playlist/delete ${PL} → HTTP $(yt playlist/delete "$(jq -nc --arg p "${PL}" '{playlistId:$p}')") $(ok)"
  for i in 1 2 3 4 5 6; do sleep 2; yt browse '{"browseId":"FEmusic_liked_playlists"}' >/dev/null; jq -e --arg b "VL${PL}" '[.. | objects | .browseId? // empty] | index($b) == null' "${BODY}" >/dev/null && { echo "   列表 $((i*2)) s 後已消失"; return 0; }; done
  echo "   ⚠ 12 s 後列表仍有 ${PL},請手動確認"
}
trap cleanup EXIT

MAT=($(awk -F'\t' '$1!="-" {print $1}' "${OUT}/04-rows.tsv" | awk '!seen[$0]++'))
if [ "${MODE}" = "cap" ] || [ "${MODE}" = "replace" ] || [ "${MODE}" = "big" ]; then
  # 素材不夠就再讀第二、第三份清單湊(只讀)
  for extra in $(awk -F'\t' '$1 ~ /^VL/ && $1 != "VLLM" && $1 !~ /^VLRD/ { n=0; if (match($3, /[0-9,]+ (songs|tracks)/)) { n=substr($3, RSTART, RLENGTH); gsub(/[^0-9]/, "", n) } print n "\t" substr($1, 3) }' "${OUT}/03-playlists.tsv" | sort -rn | sed -n '2,3p' | cut -f2); do
    rows "${extra}" > "${OUT}/cap-extra-${extra}.tsv" || true
    MAT=($(printf '%s\n' "${MAT[@]}" "$(awk -F'\t' '$1!="-" {print $1}' "${OUT}/cap-extra-${extra}.tsv")" | awk 'NF && !seen[$0]++'))
  done
  if [ "${MODE}" = "big" ]; then
    # ⑤‴(PR #118 review):(1) rename 併進同一個 actions 陣列還原子嗎;(2) 整批取代的上限——用重複的 id 把清單堆到 2000 / 5000 首(5000 是 YouTube 的清單上限)。
    echo "⑤‴ rename 併進同一請求 + 整批取代上限(素材 ${#MAT[@]} 首,尺寸 ${BIG_SIZES:-2000 5000})"
    NAME="capy-probe-$(date +%s)"; code="$(yt playlist/create "$(jq -nc --arg t "${NAME}" '{title:$t, privacyStatus:"PRIVATE"}')")"; PL="$(jq -r '.playlistId // empty' "${BODY}")"
    [ -n "${PL}" ] || { echo "建不出清單:$(head -c 200 "${BODY}")"; exit 1; }; echo "   playlist/create → HTTP ${code} id=${PL}"; sleep 3
    # (1) rename + REMOVE + ADD 同一請求
    code="$(edit "$(adds "${MAT[@]:0:20}")")"; st="$(ok)"; echo "   先放 20 首 → HTTP ${code} ${st};讀回 $(count_wait 20)"
    rev=($(printf '%s\n' "${MAT[@]:0:20}" | tail -r))
    code="$(edit "$(jq -nc --arg t "${NAME}-renamed" --argjson r "$(removes_all)" --argjson a "$(adds "${rev[@]}")" '[{action:"ACTION_SET_PLAYLIST_NAME", playlistName:$t}] + $r + $a')")"; st="$(ok)"
    after="$(vids)"; sleep 4; after2="$(vids)"
    if [ "${after2}" = "$(printf '%s ' "${rev[@]}")" ]; then order="順序 = 反序 ✓"; else order="順序不是反序 ✗"; fi
    rows "${PL}" >/dev/null; title="$(jq -r '[.. | objects | (.musicResponsiveHeaderRenderer? // .musicDetailHeaderRenderer? // empty) | .title.runs[0].text // empty] | first // "-"' "${OUT}/rows-first.json")"
    echo "   rename + REMOVE 20 + ADD 20(反序)同一請求 → HTTP ${code} ${st};${order};標題:${title}(立刻讀:$(printf '%s' "${after}" | cut -c1-24)…)"
    bad=($(printf '%s\n' "${MAT[@]:0:10}" "zzzzzzzzzzz" "${MAT[@]:10:10}"))
    code="$(edit "$(jq -nc --arg t "${NAME}-bad" --argjson r "$(removes_all)" --argjson a "$(adds "${bad[@]}")" '[{action:"ACTION_SET_PLAYLIST_NAME", playlistName:$t}] + $r + $a')")"; st="$(ok)"
    sleep 4; rows "${PL}" >/dev/null; title2="$(jq -r '[.. | objects | (.musicResponsiveHeaderRenderer? // .musicDetailHeaderRenderer? // empty) | .title.runs[0].text // empty] | first // "-"' "${OUT}/rows-first.json")"
    echo "   rename + REMOVE + ADD(夾壞 id)同一請求 → HTTP ${code} ${st};標題:${title2}(要還是 ${NAME}-renamed);列數 $(count)"
    code="$(edit "$(removes_all)")"; count_wait 0 >/dev/null
    # (2) 大清單:用重複的 id 堆(ADD 帶 dedupeOption),每批 500
    for n in ${BIG_SIZES:-2000 5000}; do
      pool=(); while [ "${#pool[@]}" -lt "${n}" ]; do pool+=("${MAT[@]}"); done; pool=("${pool[@]:0:${n}}")
      T0=$(date +%s); fail=0
      for ((i=0; i<n; i+=500)); do code="$(edit "$(adds "${pool[@]:i:500}")")"; st="$(ok)"; if [ "${code}" != "200" ] || [ "${st}" != "STATUS_SUCCEEDED" ]; then echo "   堆到 ${n}:第 $((i/500+1)) 批 ADD 500 → HTTP ${code} ${st}"; fail=1; break; fi; done
      [ "${fail}" = "1" ] && { edit "$(removes_all)" >/dev/null; count_wait 0 >/dev/null; continue; }
      echo "   堆到 ${n} 首(ADD 每批 500,$(( $(date +%s) - T0 )) s)→ 讀回 $(count_wait "${n}")"
      revp=($(printf '%s\n' "${pool[@]}" | tail -r))
      T1=$(date +%s); code="$(edit "$(jq -nc --argjson r "$(removes_all)" --argjson a "$(adds "${revp[@]}")" '$r + $a')")"; st="$(ok)"; keep "05b-replace-${n}"
      sleep 5; got="$(count_wait "${n}")"; first3="$(vids | cut -d' ' -f1-3)"; want3="${revp[0]} ${revp[1]} ${revp[2]}"
      echo "   REMOVE ${n} + ADD ${n}(反序)一個請求($((n*2)) 個 action)→ HTTP ${code} ${st}、$(( $(date +%s) - T1 )) s;讀回 ${got};前三首 ${first3}(要 ${want3})"
      T2=$(date +%s); code="$(edit "$(removes_all)")"; st="$(ok)"; echo "      REMOVE 全部(${n} 個 action)→ HTTP ${code} ${st}、$(( $(date +%s) - T2 )) s;讀回 $(count_wait 0)"
    done
    exit 0
  fi
  if [ "${MODE}" = "replace" ]; then
    # ⑤″ 真尺寸的整批取代(T2 前的最後一個未知,PR #117 review / advisor):REMOVE 全部 + ADD 全部一個請求在 N=478、640 是不是原子;
    #     大請求裡夾一個壞 id 是整包拒收還是半套用;灰掉(下架)的列能不能 ADD 回去;同一首兩份的 setVideoId 是否不同。
    echo "⑤″ 整批取代(素材 ${#MAT[@]} 首,尺寸 ${REPLACE_SIZES:-478 640})"
    NAME="capy-probe-$(date +%s)"; code="$(yt playlist/create "$(jq -nc --arg t "${NAME}" '{title:$t, privacyStatus:"PRIVATE"}')")"; PL="$(jq -r '.playlistId // empty' "${BODY}")"
    [ -n "${PL}" ] || { echo "建不出清單:$(head -c 200 "${BODY}")"; exit 1; }; echo "   playlist/create → HTTP ${code} id=${PL}"; sleep 3
    for n in ${REPLACE_SIZES:-478 640}; do
      [ "${n}" -le "${#MAT[@]}" ] || { echo "   ${n}:素材只有 ${#MAT[@]} 首,略過"; continue; }
      code="$(edit "$(adds "${MAT[@]:0:${n}}")")"; st="$(ok)"; echo "   先放 ${n} 首 → HTTP ${code} ${st};讀回 $(count_wait "${n}")"
      rev=($(printf '%s\n' "${MAT[@]:0:${n}}" | tail -r))
      T1=$(date +%s); code="$(edit "$(jq -nc --argjson r "$(removes_all)" --argjson a "$(adds "${rev[@]}")" '$r + $a')")"; st="$(ok)"; keep "05r-replace-${n}"
      after="$(vids)"
      if [ "${after}" = "$(printf '%s ' "${rev[@]}")" ]; then order="順序 = 反序 ✓"; else order="順序不是反序 ✗"; fi
      echo "   REMOVE ${n} + ADD ${n}(反序)一個請求($((n*2)) 個 action)→ HTTP ${code} ${st}、$(( $(date +%s) - T1 )) s;讀回 $(count_wait "${n}");${order}"
      code="$(edit "$(removes_all)")"; st="$(ok)"; echo "      REMOVE 全部 → HTTP ${code} ${st};讀回 $(count_wait 0)"
    done
    m=400; [ "${m}" -le "${#MAT[@]}" ] || m="${#MAT[@]}"
    code="$(edit "$(adds "${MAT[@]:0:${m}}")")"; st="$(ok)"; echo "   壞 id 測試:先放 ${m} 首 → HTTP ${code} ${st};讀回 $(count_wait "${m}")"
    before="$(vids)"
    bad=($(printf '%s\n' "${MAT[@]:0:$((m/2))}" "zzzzzzzzzzz" "${MAT[@]:$((m/2)):$((m-m/2))}"))
    code="$(edit "$(jq -nc --argjson r "$(removes_all)" --argjson a "$(adds "${bad[@]}")" '$r + $a')")"; st="$(ok)"; keep "05r-bad-id"
    after="$(vids)"
    if [ "${after}" = "${before}" ]; then atom="清單原封不動(原子 ✓)"; else atom="清單變了(半套用 ✗):現在 $(count) 列"; fi
    echo "   REMOVE ${m} + ADD ${m}(中間夾一個壞 id)一個請求 → HTTP ${code} ${st};${atom}"
    code="$(edit "$(removes_all)")"; count_wait 0 >/dev/null
    greys=($(awk -F'\t' '$5=="grey" && $1!="-" {print $1}' "${OUT}/04-rows.tsv" | head -n 3))
    if [ "${#greys[@]}" -gt 0 ]; then
      code="$(edit "$(adds "${greys[@]}")")"; st="$(ok)"; echo "   灰掉(下架)的列 ${#greys[@]} 首 ADD 回去 → HTTP ${code} ${st};讀回 $(count_wait "${#greys[@]}")"
      rows "${PL}" | cut -f1,4,5 | sed 's/^/     /'
      edit "$(removes_all)" >/dev/null; count_wait 0 >/dev/null
    else
      echo "   這份清單沒有灰掉的列,略過"
    fi
    code="$(edit "$(adds "${MAT[0]}" "${MAT[0]}" "${MAT[1]}" "${MAT[0]}")")"; st="$(ok)"
    echo "   同一首放三份 → HTTP ${code} ${st};列數 $(count_wait 4);setVideoId 唯一數:$(rows "${PL}" | cut -f2 | sort -u | wc -l | tr -d ' ')(要是 4)"
    exit 0
  fi
  echo "⑤′ 單請求 ADD 上限與讀回延遲(素材 ${#MAT[@]} 首,尺寸 ${CAP_SIZES:-466 500 640})"
  NAME="capy-probe-$(date +%s)"; code="$(yt playlist/create "$(jq -nc --arg t "${NAME}" '{title:$t, privacyStatus:"PRIVATE"}')")"; PL="$(jq -r '.playlistId // empty' "${BODY}")"
  [ -n "${PL}" ] || { echo "建不出清單:$(head -c 200 "${BODY}")"; exit 1; }; echo "   playlist/create → HTTP ${code} id=${PL}"; sleep 3
  for n in ${CAP_SIZES:-466 500 640}; do
    [ "${n}" -le "${#MAT[@]}" ] || { echo "   ${n}:素材只有 ${#MAT[@]} 首,略過"; continue; }
    code="$(edit "$(adds "${MAT[@]:0:${n}}")")"; st="$(ok)"; nres="$(jq '.playlistEditResults | length' "${BODY}")"
    echo "   ADD ${n} → HTTP ${code} ${st};edit 結果 ${nres} 筆;讀回 $(count_wait "${n}");列表副標:$(grid_count)"
    code="$(edit "$(removes_all)")"; st="$(ok)"; echo "      REMOVE 全部 → HTTP ${code} ${st};讀回 $(count_wait 0)"
  done
  exit 0
fi
[ "${#MAT[@]}" -ge 3 ] || { echo "素材不足(清單 ${PL_ID} 只有 ${#MAT[@]} 首可用)" >&2; exit 1; }
MAXN="${PROBE_MAX:-${#MAT[@]}}"; [ "${MAXN}" -le "${#MAT[@]}" ] || MAXN="${#MAT[@]}"
echo "⑤ 寫端(素材 ${#MAT[@]} 首,寫入上限 ${MAXN})"
NAME="capy-probe-$(date +%s)"
T0=$(date +%s); code="$(yt playlist/create "$(jq -nc --arg t "${NAME}" '{title:$t, privacyStatus:"PRIVATE"}')")"; keep 05a-create
PL="$(jq -r '.playlistId // empty' "${BODY}")"; st="$(ok)"; echo "   5a playlist/create → HTTP ${code} ${st} id=${PL:-?}"
[ -n "${PL}" ] || { echo "   建不出清單,回應:$(head -c 300 "${BODY}")"; PL=""; exit 1; }
for i in $(seq 1 30); do yt browse '{"browseId":"FEmusic_liked_playlists"}' >/dev/null; jq -e --arg b "VL${PL}" '[.. | objects | .browseId? // empty] | index($b) != null' "${BODY}" >/dev/null && { echo "   列表 $(( $(date +%s) - T0 )) s 後出現"; break; }; sleep 1; done

n=100; [ "${n}" -le "${MAXN}" ] || n="${MAXN}"
while :; do
  code="$(edit "$(adds "${MAT[@]:0:${n}}")")"; keep "05b-add-${n}"; st="$(ok)"; nres="$(jq '.playlistEditResults | length' "${BODY}")"
  echo "   5b ADD ${n} 首一個請求 → HTTP ${code} ${st};edit 結果 ${nres} 筆;讀回 $(count_wait "${n}");列表副標:$(grid_count)"
  [ "${code}" = "200" ] && [ "${st}" = "STATUS_SUCCEEDED" ] || break
  code="$(edit "$(removes_all)")"; st="$(ok)"; echo "      REMOVE 全部一個請求 → HTTP ${code} ${st};讀回 $(count_wait 0)"
  [ "${n}" -ge "${MAXN}" ] && break
  n=$((n*2)); [ "${n}" -le "${MAXN}" ] || n="${MAXN}"
done

m=50; [ "${m}" -le "${MAXN}" ] || m="${MAXN}"
if [ "$(count_wait 0)" != "0 列(等了 0 s)" ]; then edit "$(removes_all)" >/dev/null; count_wait 0 >/dev/null; fi # 從空清單開始
code="$(edit "$(adds "${MAT[@]:0:${m}}")")"; st="$(ok)"; echo "   5c 先放 ${m} 首 → HTTP ${code} ${st}"
before="$(vids)"
rev=($(printf '%s\n' "${MAT[@]:0:${m}}" | tail -r))
code="$(edit "$(jq -nc --argjson r "$(removes_all)" --argjson a "$(adds "${rev[@]}")" '$r + $a')")"; keep 05c-replace; st="$(ok)"
after="$(vids)"
if [ "${after}" = "$(printf '%s ' "${rev[@]}")" ]; then order="順序 = 反序 ✓"; else order="順序不是反序 ✗(before: ${before:0:60}… after: ${after:0:60}…)"; fi
echo "      REMOVE ${m} + ADD ${m}(反序)同一請求 → HTTP ${code} ${st};讀回 $(count_wait "${m}");${order}"

code="$(edit "$(jq -nc --arg a "${MAT[1]}" --arg b "${MAT[2]}" '[{action:"ACTION_ADD_VIDEO", addedVideoId:$a, dedupeOption:"DEDUPE_OPTION_SKIP"},{action:"ACTION_ADD_VIDEO", addedVideoId:"zzzzzzzzzzz", dedupeOption:"DEDUPE_OPTION_SKIP"},{action:"ACTION_ADD_VIDEO", addedVideoId:$b, dedupeOption:"DEDUPE_OPTION_SKIP"}]')")"; keep 05d-bad_id; st="$(ok)"
echo "   5d 好 / 壞 / 好 三個 ADD 一個請求 → HTTP ${code} ${st};讀回 $(count) 列(原子 = 還是 ${m};部分套用 = ${m}+2)"

c0="$(count)"
code="$(edit "$(jq -nc --arg a "${MAT[0]}" '[{action:"ACTION_ADD_VIDEO", addedVideoId:$a}]')")"; st="$(ok)"; echo "   5e 再加已在清單裡的一首、不帶 dedupeOption → HTTP ${code} ${st};列數 ${c0} → $(count)"
code="$(edit "$(jq -nc --arg a "${MAT[0]}" '[{action:"ACTION_ADD_VIDEO", addedVideoId:$a, dedupeOption:"DEDUPE_OPTION_SKIP"}]')")"; st="$(ok)"; echo "      帶 DEDUPE_OPTION_SKIP → HTTP ${code} ${st};列數 → $(count)"

code="$(edit "$(jq -nc --arg t "${NAME}-renamed" '[{action:"ACTION_SET_PLAYLIST_NAME", playlistName:$t}]')")"; keep 05f-rename; st="$(ok)"
rows "${PL}" >/dev/null; echo "   5f 改名 → HTTP ${code} ${st};讀回標題:$(jq -r '[.. | objects | (.musicResponsiveHeaderRenderer? // .musicDetailHeaderRenderer? // empty) | .title.runs[0].text // empty] | first // "-"' "${OUT}/rows-first.json")"

first="$(rows "${PL}" | head -n1)"; third="$(rows "${PL}" | sed -n 3p)"
code="$(edit "$(jq -nc --arg s "$(printf '%s' "${first}" | cut -f2)" --arg t "$(printf '%s' "${third}" | cut -f2)" '[{action:"ACTION_MOVE_VIDEO_BEFORE", setVideoId:$s, movedSetVideoIdSuccessor:$t}]')")"; st="$(ok)"
echo "   5g 第 1 列搬到第 3 列前 → HTTP ${code} ${st};現在前 3 列:$(vids | cut -d' ' -f1-3)"

echo "⑦ 上傳的歌"
code="$(yt browse '{"browseId":"FEmusic_library_privately_owned_tracks"}')"; keep 07-uploads
UP="$(jq -r "${ROWS}" "${BODY}" | awk -F'\t' '$1!="-" {print $1; exit}')"
if [ -n "${UP}" ]; then code="$(edit "$(adds "${UP}")")"; st="$(ok)"; echo "   有上傳的歌 ${UP};ADD 進測試清單 → HTTP ${code} ${st};列數 → $(count)"; else echo "   HTTP ${code};這個帳號沒有上傳的歌,略過"; fi
echo "(寫端完成;結尾自動 playlist/delete)"
