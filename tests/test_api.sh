#!/bin/sh

set -eu

root_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
test_dir=/tmp/warp-test
fixture="$root_dir/tests/fixtures/usque.json"
api="$root_dir/root/usr/libexec/warp-api"
mock_curl="$root_dir/tests/mocks/curl"
mock_jsonfilter="$root_dir/tests/mocks/jsonfilter"

mkdir -p "$test_dir"
chmod 700 "$test_dir"
cp "$fixture" "$test_dir/usque.json"

WARP_CURL_BIN="$mock_curl" \
WARP_JSONFILTER_BIN="$mock_jsonfilter" \
MOCK_CURL_MODE=delete_valid \
	"$api" unregister "$test_dir/usque.json"

printf '{"id":"11111111-2222-4333-8444-555555555555","access_token":"valid-token\\nInjected: header"}\n' >"$test_dir/malicious.json"
error=$(WARP_CURL_BIN="$mock_curl" WARP_JSONFILTER_BIN="$mock_jsonfilter" \
	"$api" unregister "$test_dir/malicious.json" 2>&1 || true)
[ "$error" = invalid_registration ]

if "$api" register >/dev/null 2>&1; then
	echo 'removed registration action is still accepted' >&2
	exit 1
fi

rm -f "$test_dir/usque.json" "$test_dir/malicious.json"
printf '%s\n' 'API adapter tests: OK'
