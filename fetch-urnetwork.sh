#!/usr/bin/env bash
# Downloads the URnetwork trees this core links, then applies the Shield dial
# patch. Runs on the GitHub macOS runner. Nothing here is cloned on Windows.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")" && pwd)
DEST="$ROOT/urnetwork"
OVERLAY="$ROOT/urnetwork-overlay"

SDK_SHA=40432c72288d8dce09a68622ee7afff90925f80a
CONNECT_SHA=44d007e48cb3b26f17e3b9d4690fe1c2046b7106
GLOG_SHA=80a11b434ae90ae2221deb932daa21913919875a
ICONS_SHA=325750b38314313dc5f44c880ab6f12f6c1ecb3c

fetch_sha() {
  local url="$1" sha="$2" dir="$3"
  if [ -d "$dir/.git" ] && [ "$(git -C "$dir" rev-parse HEAD)" = "$sha" ]; then
    return
  fi
  rm -rf "$dir"
  mkdir -p "$dir"
  git -C "$dir" init
  git -C "$dir" remote add origin "$url"
  git -C "$dir" fetch --depth 1 origin "$sha"
  git -C "$dir" checkout --detach FETCH_HEAD
}

mkdir -p "$DEST"
fetch_sha https://github.com/urnetwork/sdk.git "$SDK_SHA" "$DEST/sdk"
fetch_sha https://github.com/urnetwork/connect.git "$CONNECT_SHA" "$DEST/connect"
fetch_sha https://github.com/urnetwork/glog.git "$GLOG_SHA" "$DEST/glog"
fetch_sha https://github.com/urnetwork/goidenticons.git "$ICONS_SHA" "$DEST/goidenticons"

if ! grep -q 'func SetUpstreamSocks' "$DEST/sdk/sdk.go"; then
  patch -p1 -d "$DEST/sdk" < "$OVERLAY/sdk.patch"
fi
if [ ! -f "$DEST/connect/upstream_socks.go" ]; then
  patch -p1 -d "$DEST/connect" < "$OVERLAY/connect.patch"
fi
cp "$OVERLAY/upstream_socks.go" "$DEST/connect/upstream_socks.go"
echo "==> urnetwork sources at $DEST"
