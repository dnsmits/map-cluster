#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${1:-http://localhost:8080}"
DURATION="${DURATION:-10s}"
INTERVAL="${INTERVAL:-120ms}"
BBOX="${BBOX:--125,25,-66,49}"
ZOOM="${ZOOM:-4}"
MODE="${MODE:-c}"
FORMAT="${FORMAT:-msgpack}"

WS_URL="${WS_URL:-${BASE_URL/http:/ws:}/ws/stream}"
FEATURES_URL="${BASE_URL}/api/v1/features?bbox=${BBOX}&zoom=${ZOOM}&mode=$([[ "${MODE}" == "h" ]] && echo heatmap || echo cluster)"

echo "HTTP benchmark target: ${FEATURES_URL}"
echo "WS benchmark target:   ${WS_URL}"
echo

echo "== HTTP JSON (no gzip) =="
curl -sS -o /dev/null -w "status=%{http_code} bytes=%{size_download} total=%{time_total}s\n" "${FEATURES_URL}"

echo "== HTTP JSON (gzip) =="
curl --compressed -sS -o /dev/null -w "status=%{http_code} bytes=%{size_download} total=%{time_total}s\n" "${FEATURES_URL}"

echo
echo "== WS Stream Benchmark =="
cd "$(dirname "$0")/.."
go run ./cmd/wsbench -url "${WS_URL}" -duration "${DURATION}" -interval "${INTERVAL}" -bbox "${BBOX}" -zoom "${ZOOM}" -mode "${MODE}" -format "${FORMAT}"
