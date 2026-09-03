#!/usr/bin/env bash
# P0-1:驗證 Apple catalog search 回傳 ISRC(docs/ARCHITECTURE.md §9 P0-1)。
# 前置:capy auth login apple(見 README);DT=capy debug apple-token;MUT=capy debug apple-token --user
# 用法:scripts/p0/p0-1-isrc.sh [搜尋詞…]
set -euo pipefail
CAPY="${CAPY:-go run ./cmd/capy}"
BASE="${CAPY_APPLE_API_BASE:-https://amp-api.music.apple.com/v1}"
QUERY="${*:-五月天 派對動物}"
DT="$(${CAPY} debug apple-token)"
ENC="$(jq -rn --arg s "${QUERY}" '$s|@uri')"
resp="$(curl -sS -H "Authorization: Bearer ${DT}" -H "Origin: https://music.apple.com" \
  "${BASE}/catalog/tw/search?types=songs&limit=5&term=${ENC}")"
echo "── songs(id / ISRC / 曲名)──"
echo "${resp}" | jq -r '.results.songs.data[] | [.id, (.attributes.isrc // "❌ NO-ISRC"), .attributes.name] | @tsv'
echo
echo "P0-1 判定:ISRC 欄非 ❌ 即通過。結果記錄到 docs/ARCHITECTURE.md §9 P0-1(附錄 A 有格式)。"
