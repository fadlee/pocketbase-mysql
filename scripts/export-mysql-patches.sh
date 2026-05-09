#!/usr/bin/env bash
set -euo pipefail

base_ref="${1:-${MYSQL_PATCH_BASE:-7829cb3b}}"
out_dir="${MYSQL_PATCH_DIR:-patches/mysql-poc}"

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

rm -rf "$out_dir"
mkdir -p "$out_dir"

git format-patch --output-directory "$out_dir" "$base_ref"..HEAD

cat > "$out_dir/README.md" <<EOF
# MySQL PoC Patch Stack

Generated from:

- Base ref: \`$base_ref\`
- Head ref: \`$(git rev-parse --short HEAD)\`

Apply to a clean upstream worktree with:

\`\`\`sh
scripts/apply-mysql-patches.sh $out_dir
\`\`\`
EOF

echo "Exported MySQL patch stack to $out_dir"
