#!/usr/bin/env bash
set -euo pipefail

patch_dir="${1:-${MYSQL_PATCH_DIR:-patches/mysql-poc}}"

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

if [ -n "$(git status --porcelain)" ]; then
	echo "Refusing to apply patches on a dirty worktree." >&2
	exit 1
fi

shopt -s nullglob
patches=("$patch_dir"/*.patch)
if [ "${#patches[@]}" -eq 0 ]; then
	echo "No patch files found in $patch_dir" >&2
	exit 1
fi

git am "${patches[@]}"
