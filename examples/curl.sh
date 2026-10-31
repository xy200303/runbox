#!/usr/bin/env bash
# A small curl walkthrough. Requires bash and curl.
#
#   RUNBOX_API_KEY=... RUNBOX_IDENTITY=demo.alice RUNBOX_URL=http://localhost:8099 ./examples/curl.sh
#   or RUNBOX_NAMESPACE=tenant-a RUNBOX_PRINCIPAL=user-42
set -euo pipefail

: "${RUNBOX_URL:=http://localhost:8099}"
: "${RUNBOX_API_KEY:?set RUNBOX_API_KEY}"
: "${RUNBOX_IDENTITY:=demo.alice}"

auth=(
  -H "Authorization: Bearer ${RUNBOX_API_KEY}"
  -H "X-Runbox-Identity: ${RUNBOX_IDENTITY}"
)

echo "== health"
curl -fsS "${RUNBOX_URL}/health"
echo
echo "== capabilities"
curl -fsS "${auth[@]}" "${RUNBOX_URL}/v1/capabilities"
echo
echo "== exec"
result=$(curl -fsS "${auth[@]}" \
  -H 'Content-Type: application/json' \
  -d '{"command":"mkdir -p out && echo hello > out/hello.txt","timeout_seconds":30}' \
  "${RUNBOX_URL}/v1/exec")
echo "${result}"

echo "== download out/hello.txt by path"
curl -fsS "${auth[@]}" -o hello.txt "${RUNBOX_URL}/v1/path?path=out/hello.txt"
cat hello.txt

echo "== list out/"
curl -fsS "${auth[@]}" "${RUNBOX_URL}/v1/list?path=out"
echo
