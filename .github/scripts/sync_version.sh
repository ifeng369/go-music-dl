#!/usr/bin/env bash
# 把版本号同步到 core/version.go、fnos/manifest、README.md。
# 用法: bash .github/scripts/sync_version.sh 1.2.4
set -euo pipefail

version="${1:-}"
if [ -z "$version" ]; then
  echo "::error::缺少版本号"
  exit 1
fi

sed -i -E "s/(const AppVersion = \")[^\"]*(\")/\1${version}\2/" core/version.go
sed -i -E "s/^(version[[:space:]]*=[[:space:]]*)[^[:space:]\r]+/\1${version}/" fnos/manifest
sed -i -E "s/(--version )[0-9][0-9A-Za-z._-]*/\1${version}/; s/(--tag v)[0-9][0-9A-Za-z._-]*/\1${version}/" README.md

echo "Synced version ${version}:"
grep -n "AppVersion" core/version.go
grep -n "^version" fnos/manifest
grep -n "update_fnpack.py --repo" README.md
