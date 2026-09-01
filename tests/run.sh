#!/bin/sh

set -eu

root_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

python3 "$root_dir/tests/test_static.py"
"$root_dir/tests/test_api.sh"

printf '%s\n' 'All host tests passed.'
