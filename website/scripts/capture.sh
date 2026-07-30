#!/usr/bin/env bash
# Capture the product stage's screenshots from a real run.
#
# The landing page shows the results server itself, not a drawing of it. This
# serves runs/load-local, points a headless Chromium at the four views, and
# writes them into src/assets/product/. Re-run it when the report UI changes.
#
#   website/scripts/capture.sh
#
# Requires a Chromium-family browser; set BROWSER to override the search.

set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
out="$repo/website/src/assets/product"
addr="127.0.0.1:7581"

# The stress run that makes the point: 2,000 flow-runs, 49.2% throttled, and
# a threshold gate that still passed.
run="20260727T130340.321822000Z"

find_browser() {
  if [[ -n "${BROWSER:-}" ]]; then
    echo "$BROWSER"
    return
  fi
  local candidates=(
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
    "/Applications/Chromium.app/Contents/MacOS/Chromium"
    "/Applications/Brave Browser.app/Contents/MacOS/Brave Browser"
    "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge"
  )
  for candidate in "${candidates[@]}"; do
    [[ -x "$candidate" ]] && { echo "$candidate"; return; }
  done
  for candidate in google-chrome chromium chromium-browser; do
    command -v "$candidate" >/dev/null && { command -v "$candidate"; return; }
  done
  echo "no Chromium-family browser found; set BROWSER=/path/to/chrome" >&2
  exit 1
}

browser="$(find_browser)"
mkdir -p "$out"

go build -o "$repo/.bin/flowbench" "$repo/cmd/flowbench"
"$repo/.bin/flowbench" serve -store load="$repo/runs/load-local" -addr "$addr" &
server=$!
trap 'kill "$server" 2>/dev/null || true' EXIT

for _ in $(seq 1 40); do
  curl -sf -o /dev/null "http://$addr/" && break
  sleep 0.25
done

shoot() {
  local name="$1" path="$2" size="$3"
  "$browser" --headless --disable-gpu --hide-scrollbars \
    --force-device-scale-factor=2 --window-size="$size" \
    --screenshot="$out/$name.png" \
    "http://$addr/p/load/runs/$run$path" 2>/dev/null
  echo "captured $name"
}

# Each view is deep-linked to a selection, so the detail rail carries content
# instead of its empty state — an unselected app is a screenshot of nothing.
shoot dashboard "?at=25" 1440,940
shoot flame "/flame?frame=flow%3Acheckout_pressure.checkout.http_call" 1440,880
shoot waterfall "/waterfall?span=0.0.0" 1440,940
shoot outcomes "/outcomes?outcome=throttled" 1440,940

# Card-sized element crops for the feature bento (shot.mjs clips to one
# results-server card). The flame crop trims the tall empty canvas below the
# four frame rows.
export BROWSER="$browser"
node "$repo/website/scripts/shot.mjs" \
  "http://$addr/p/load/runs/$run/outcomes?outcome=throttled" 1440x1200 \
  "$out/card-outcomes.png" "--selector=.main section.card:first-of-type"
node "$repo/website/scripts/shot.mjs" \
  "http://$addr/p/load/runs/$run/flame?frame=flow%3Acheckout_pressure.checkout.http_call" \
  1280x1000 /tmp/card-flame-raw.png "--selector=.flame-workspace"
node -e "
  const sharp = require('$repo/website/node_modules/sharp');
  sharp('/tmp/card-flame-raw.png')
    .extract({ left: 0, top: 0, width: 2408, height: 1290 })
    .toFile('$out/card-flame.png');
"
