#!/bin/sh
# Vendors the Pegasus credential catalog byte for byte and prints its SHA-256.
# usage: sync-catalog.sh <path to credential-transport-catalog.json>
# After a re-vendor: update pinnedCatalogSHA256 in catalog_test.go and re-run
# the golden cases with gen-cases.py (the Pegasus hook is the oracle), and check
# parity: every case, golden and random, must match the hook byte for byte.
set -eu
[ $# -eq 1 ] || { echo "usage: $0 <catalog.json>" >&2; exit 2; }
dir=$(cd "$(dirname "$0")" && pwd)
cp "$1" "$dir/catalog.json"
sha256sum "$dir/catalog.json" | cut -d' ' -f1
