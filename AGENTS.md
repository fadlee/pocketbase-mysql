# AGENTS.md

## Project Overview

PocketBase MySQL fork — adds MySQL-backed data DB support on top of upstream PocketBase releases. The fork is maintained as a replayable patch stack.

## Key Architecture

- Upstream baseline: PocketBase `v0.38.2`
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
2. `git tag -a v0.38.x-mysql.N -m "..."`
3. `git push origin mysql/main && git push origin <tag>`
4. `gh release create <tag> --repo fadlee/pocketbase-mysql --title "<tag>" --notes "..."`
5. Workflows auto-trigger: `release-pocketbase-mysql` (zip binaries), `publish-ghcr` (Docker image)
