#!/usr/bin/env bash
set -euo pipefail

: "${VERSION:?VERSION is required}"
: "${AMD64_URL:?AMD64_URL is required}"
: "${AMD64_SHA256:?AMD64_SHA256 is required}"
: "${ARM64_URL:?ARM64_URL is required}"
: "${ARM64_SHA256:?ARM64_SHA256 is required}"
: "${OUTPUT:?OUTPUT is required}"

repository_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)

escape_sed() {
  printf '%s' "$1" | sed 's/[&|]/\\&/g'
}

sed \
  -e "s|{{VERSION}}|$(escape_sed "$VERSION")|g" \
  -e "s|{{AMD64_URL}}|$(escape_sed "$AMD64_URL")|g" \
  -e "s|{{AMD64_SHA256}}|$(escape_sed "$AMD64_SHA256")|g" \
  -e "s|{{ARM64_URL}}|$(escape_sed "$ARM64_URL")|g" \
  -e "s|{{ARM64_SHA256}}|$(escape_sed "$ARM64_SHA256")|g" \
  "$repository_root/packaging/homebrew/camp.rb.tmpl" >"$OUTPUT"
