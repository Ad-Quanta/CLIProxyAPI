#!/usr/bin/env bash
# Phase 1 model-fallback integration test.
#
# Runs an isolated proxy container (no Home mode, no production volumes) against
# a local mock upstream, then verifies non-stream, stream, and no-fallback paths.
set -euo pipefail

cd "$(dirname "$0")"
ROOT_DIR="$(cd ../.. && pwd)"
COMPOSE_PROJECT="cpa-fallback-test"
COMPOSE=(docker compose -f compose.test.yml -p "${COMPOSE_PROJECT}")
PROXY_URL="http://127.0.0.1:18317"
MOCK_URL="http://127.0.0.1:18318"
API_KEY="fallback-test-key"
MOCK_PID=""
TMP_DIR="$(mktemp -d)"

cleanup() {
  if [[ -n "${MOCK_PID}" ]] && kill -0 "${MOCK_PID}" 2>/dev/null; then
    kill "${MOCK_PID}" 2>/dev/null || true
    wait "${MOCK_PID}" 2>/dev/null || true
  fi
  "${COMPOSE[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
  rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  echo "--- proxy logs (tail) ---" >&2
  tail -n 80 logs/*.log 2>/dev/null >&2 || true
  exit 1
}

wait_for() {
  local url="$1" attempts="${2:-150}"
  for _ in $(seq 1 "${attempts}"); do
    if curl -sf --max-time 1 "${url}" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.2
  done
  return 1
}

mkdir -p auths logs
: > mock.log

echo "==> building image cliproxy-api:fallback-test"
docker build -q -t cliproxy-api:fallback-test "${ROOT_DIR}"

echo "==> starting mock upstream on ${MOCK_URL}"
python3 -u mock_upstream.py >mock.log 2>&1 &
MOCK_PID=$!
wait_for "${MOCK_URL}/healthz" || fail "mock upstream did not become healthy"

echo "==> starting isolated proxy on ${PROXY_URL}"
"${COMPOSE[@]}" up -d --no-build
proxy_ready=false
for _ in $(seq 1 150); do
  if curl -sf --max-time 1 -H "Authorization: Bearer ${API_KEY}" "${PROXY_URL}/v1/models" >/dev/null 2>&1; then
    proxy_ready=true
    break
  fi
  sleep 0.2
done
[[ "${proxy_ready}" == "true" ]] || fail "proxy did not become ready"

reset_mock() {
  curl -sf -X POST "${MOCK_URL}/reset" >/dev/null || fail "mock reset failed"
}

mock_count() {
  curl -sf "${MOCK_URL}/metrics" | python3 -c "import json,sys; print(json.load(sys.stdin).get(sys.argv[1], 0))" "$1"
}

assert_counts() {
  local primary_count target_count
  primary_count="$(mock_count fallback-primary)"
  target_count="$(mock_count fallback-target)"
  [[ "${primary_count}" == "$1" ]] || fail "fallback-primary request count = ${primary_count}, want $1"
  [[ "${target_count}" == "$2" ]] || fail "fallback-target request count = ${target_count}, want $2"
}

echo "==> case 1: non-stream 429 falls back to fallback-target"
reset_mock
status="$(curl -sS -o "${TMP_DIR}/nonstream.json" -w '%{http_code}' \
  -H "Authorization: Bearer ${API_KEY}" -H 'Content-Type: application/json' \
  -d '{"model":"fallback-primary","messages":[{"role":"user","content":"hi"}]}' \
  "${PROXY_URL}/v1/chat/completions")"
[[ "${status}" == "200" ]] || fail "non-stream status = ${status}, want 200"
grep -Eq '"model"[[:space:]]*:[[:space:]]*"fallback-primary"' "${TMP_DIR}/nonstream.json" || fail "non-stream response did not preserve requested model: $(cat "${TMP_DIR}/nonstream.json")"
grep -q 'hello from fallback' "${TMP_DIR}/nonstream.json" || fail "non-stream response missing fallback content"
assert_counts 1 1

echo "==> case 2: stream 429 falls back before first byte"
reset_mock
status="$(curl -sS -N -o "${TMP_DIR}/stream.txt" -w '%{http_code}' \
  -H "Authorization: Bearer ${API_KEY}" -H 'Content-Type: application/json' \
  -d '{"model":"fallback-primary","stream":true,"messages":[{"role":"user","content":"hi"}]}' \
  "${PROXY_URL}/v1/chat/completions")"
[[ "${status}" == "200" ]] || fail "stream status = ${status}, want 200"
grep -q 'hello ' "${TMP_DIR}/stream.txt" || fail "stream response missing fallback content"
grep -q 'from fallback' "${TMP_DIR}/stream.txt" || fail "stream response missing fallback content"
grep -q '\[DONE\]' "${TMP_DIR}/stream.txt" || fail "stream response missing [DONE]"
grep -Eq '"model"[[:space:]]*:[[:space:]]*"fallback-primary"' "${TMP_DIR}/stream.txt" || fail "stream response did not preserve requested model"
assert_counts 1 1

echo "==> case 3: 400 does not fall back"
reset_mock
status="$(curl -sS -o "${TMP_DIR}/bad.json" -w '%{http_code}' \
  -H "Authorization: Bearer ${API_KEY}" -H 'Content-Type: application/json' \
  -d '{"model":"no-fallback-primary","messages":[{"role":"user","content":"hi"}]}' \
  "${PROXY_URL}/v1/chat/completions")"
[[ "${status}" == "400" ]] || fail "no-fallback status = ${status}, want 400"
bad_count="$(mock_count no-fallback-primary)"
target_count="$(mock_count fallback-target)"
[[ "${bad_count}" == "1" ]] || fail "no-fallback-primary request count = ${bad_count}, want 1"
[[ "${target_count}" == "0" ]] || fail "fallback-target request count = ${target_count}, want 0 (no fallback on 400)"

echo
echo "PASS: model fallback integration test"
