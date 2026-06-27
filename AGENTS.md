# AGENTS.md

## Project Overview

PocketBase MySQL fork — adds MySQL-backed data DB support on top of upstream PocketBase releases. The fork is maintained as a replayable patch stack.

## Key Architecture

- Upstream baseline: PocketBase `v0.39.4`
- MySQL connection via env vars: `PB_DATABASE_DRIVER=mysql` and `PB_DATABASE_DSN=<dsn>`
- Patch stack lives in `patches/mysql-poc/`
- Scripts are Node.js only (no bash/python dependency)

## Scripts

- `node scripts/apply-mysql-patches.mjs [patch-dir]` — apply patch stack
- `node scripts/export-mysql-patches.mjs <base-ref>` — export patch stack
- `node scripts/mysql-runtime-qa.mjs [options]` — runtime QA against MySQL

## Upstream Upgrade Checklist

When upgrading to a new upstream PocketBase version:

1. **Export patches first** — run `node scripts/export-mysql-patches.mjs <current-base>` before switching branches.

2. **Scan new migrations for SQLite-specific code** — grep for `sqlite_master`, `sqlite_schema`, `PRAGMA`, `randomblob`, `strftime`, `_rowid_`. Add MySQL early-return guards where needed.

3. **Skip `ui/dist` conflicts during replay** — bundle hashes always differ. Skip patches that only conflict in `ui/dist`, then run `cd ui && npm run build` once at the end.

4. **Run `go mod tidy` and commit `go.sum`** — always verify `go.sum` is committed before pushing. CI will fail on missing checksums.

5. **Test QA script locally before declaring done** — ensure the QA script uses the correct connection mechanism (env vars, not CLI flags).

6. **Review CI/CD workflows after replay** — check release packaging (zip), ghupdate owner/repo config, and workflow triggers for redundancy.

7. **Tag only after CI is green** — do not tag until all fixes are committed and the branch builds successfully on CI. Avoids re-tagging.

8. **Export patch stack as the very last step** — only after all fixes, docs updates, and workflow changes are committed. Export once, commit once.

## MySQL-Specific Patterns

- New upstream migrations that query `sqlite_master` or `sqlite_schema` need an early return:
  ```go
  if strings.EqualFold(os.Getenv("PB_DATABASE_DRIVER"), "mysql") {
      return nil
  }
  ```
- Sort expressions using `_rowid_` need a MySQL fallback resolving to `id`.
- `LIKE` filter escaping differs between SQLite and MySQL.
- Partial indexes (`WHERE` clause in `CREATE INDEX`) are not supported in MySQL — use regular indexes.
- UI source should use portable sort fields (`-created,-id`) instead of `@rowid`.

## Build & Test

```sh
go build ./...                    # compile check
go test ./...                     # unit tests (some flaky on Windows: symlink, timing)
go build -o pocketbase-mysql ./examples/base   # build binary
node scripts/mysql-runtime-qa.mjs --skip-docker --mysql-port 3306 --mysql-password "" --mysql-database pocketbase_qa
```

## Release Flow

1. Ensure all changes committed and CI green
2. `git tag -a v0.39.x-mysql.N -m "..."`
3. `git push origin mysql/main && git push origin <tag>`
4. `gh release create <tag> --repo fadlee/pocketbase-mysql --title "<tag>" --notes "..."`
5. Workflows auto-trigger: `release-pocketbase-mysql` (zip binaries), `publish-ghcr` (Docker image)

## Full Upstream Upgrade Flow

End-to-end steps when a new upstream version (e.g. `v0.39.5`) is released:

```sh
# 1. Export current patch stack
node scripts/export-mysql-patches.mjs v0.39.4

# 2. Create branch from new upstream tag
git fetch upstream
git switch --create mysql/rebase-v0.39.5 v0.39.5

# 3. Scan new migrations for SQLite-specific code
git diff v0.39.4..v0.39.5 -- migrations/ | grep -i "sqlite\|pragma\|rowid"
# Add MySQL early-return guards where needed

# 4. Apply patch stack
git am --3way patches/mysql-poc/*.patch
# Conflict in ui/dist → skip (git am --skip), regenerate later
# Conflict in Go/source → resolve manually, then git am --continue

# 5. Fix & verify
go mod tidy
go build ./...
go test ./...
cd ui && npm run build && cd ..
git add -A && git commit -m "Regenerate UI dist"

# 6. Runtime QA
# Reset the QA database first, then:
node scripts/mysql-runtime-qa.mjs --skip-docker --mysql-port 3306 --mysql-password "" --mysql-database pocketbase_qa

# 7. Update docs (README, gap-analysis, upstream-workflow) — change baseline refs

# 8. Export patch stack (once, as the very last step)
node scripts/export-mysql-patches.mjs v0.39.5
git add patches/mysql-poc/ && git commit -m "Refresh patch stack for v0.39.5"

# 9. Merge to mysql/main, tag, release
git switch mysql/main
git reset --hard mysql/rebase-v0.39.5
git tag -a v0.39.5-mysql.1 -m "PocketBase v0.39.5 MySQL fork release 1"
git push --force-with-lease origin mysql/main
git push origin v0.39.5-mysql.1
gh release create v0.39.5-mysql.1 --repo fadlee/pocketbase-mysql --title "v0.39.5-mysql.1" --notes "..."
```

Key principle: one direction, tag only once at the end after CI is green.
