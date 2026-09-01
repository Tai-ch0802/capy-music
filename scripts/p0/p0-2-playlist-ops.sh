#!/usr/bin/env bash
# P0-2:驗證 Apple Music API 能否對 library playlist 移除/重排曲目(§9 P0-2)。
# 前置:
#   1) export CAPY_APPLE_P8_PATH=… CAPY_APPLE_TEAM_ID=…
#   2) 先跑 `go run ./cmd/capy debug apple-auth`,把印出的 MUT export 成 CAPY_APPLE_MUT
# 注意:會在你的音樂庫建立測試播放清單(名稱帶時間戳)。Apple API 沒有刪除
# playlist 的端點,結束後請在 Music.app 手動刪除。
set -euo pipefail
CAPY="${CAPY:-go run ./cmd/capy}"
MUT="${CAPY_APPLE_MUT:?請先跑 capy debug apple-auth 並 export CAPY_APPLE_MUT}"
DT="$(${CAPY} debug apple-token)"
H=(-H "Authorization: Bearer ${DT}" -H "Music-User-Token: ${MUT}")
API="https://api.music.apple.com"

echo "① 取兩首 catalog 歌曲當測試素材"
# 注意:不用 mapfile(macOS 內建 bash 3.2 沒有);id 無空白,word splitting 安全
ids=($(curl -sS "${H[@]}" \
  "${API}/v1/catalog/tw/search?types=songs&limit=2&term=$(jq -rn --arg s '五月天' '$s|@uri')" \
  | jq -r '.results.songs.data[].id'))
if [ "${#ids[@]}" -lt 2 ]; then
  echo "搜尋結果不足兩首(得到 ${#ids[@]} 首),換個搜尋詞再跑" >&2
  exit 1
fi
echo "  song ids: ${ids[*]}"

NAME="capy-p0-2-$(date +%s)"
echo "② 建立測試清單 ${NAME}"
create="$(curl -sS -X POST "${H[@]}" -H 'Content-Type: application/json' \
  -d "{\"attributes\":{\"name\":\"${NAME}\",\"description\":\"capy P0-2 驗證,可刪\"}}" \
  "${API}/v1/me/library/playlists")"
PL="$(echo "${create}" | jq -r '.data[0].id')"
echo "  playlist id: ${PL}"

echo "③ 加入兩首歌"
curl -sS -o /dev/null -w '  add tracks → HTTP %{http_code}\n' -X POST "${H[@]}" \
  -H 'Content-Type: application/json' \
  -d "{\"data\":[{\"id\":\"${ids[0]}\",\"type\":\"songs\"},{\"id\":\"${ids[1]}\",\"type\":\"songs\"}]}" \
  "${API}/v1/me/library/playlists/${PL}/tracks"

echo "④ 讀回清單曲目(取得 library track id)"
tracks="$(curl -sS "${H[@]}" "${API}/v1/me/library/playlists/${PL}/tracks")"
echo "${tracks}" | jq -r '.data[] | [.id, .attributes.name] | @tsv'
LT0="$(echo "${tracks}" | jq -r '.data[0].id')"

echo "⑤ 嘗試「移除單曲」— 兩種候選端點,記錄 HTTP code:"
curl -sS -o /dev/null -w '  DELETE …/tracks/{id}       → HTTP %{http_code}\n' \
  -X DELETE "${H[@]}" "${API}/v1/me/library/playlists/${PL}/tracks/${LT0}"
curl -sS -o /dev/null -w '  DELETE …/tracks(含 body)  → HTTP %{http_code}\n' \
  -X DELETE "${H[@]}" -H 'Content-Type: application/json' \
  -d "{\"data\":[{\"id\":\"${LT0}\",\"type\":\"library-songs\"}]}" \
  "${API}/v1/me/library/playlists/${PL}/tracks"

echo "⑥ 嘗試「整批重排/覆寫」:"
curl -sS -o /dev/null -w '  PUT …/tracks(反序)       → HTTP %{http_code}\n' \
  -X PUT "${H[@]}" -H 'Content-Type: application/json' \
  -d "{\"data\":[{\"id\":\"${ids[1]}\",\"type\":\"songs\"},{\"id\":\"${ids[0]}\",\"type\":\"songs\"}]}" \
  "${API}/v1/me/library/playlists/${PL}/tracks"

echo
echo "判定:⑤/⑥ 任一回 2xx → 對應 CapPlaylistRemove / CapPlaylistReorder 成立;"
echo "全部 4xx → Apple push 走 rebuild 策略(spec §6.5 fallback)。"
echo "結果記錄:docs/ARCHITECTURE.md §1.2「Library playlist 寫入」列 + §9 P0-2(附錄 A 有格式)。"
echo "🧹 請在 Music.app 手動刪除測試清單:${NAME}"
