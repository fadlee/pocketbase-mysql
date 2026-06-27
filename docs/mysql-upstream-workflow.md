# MySQL Fork Upstream Workflow

This fork keeps MySQL support as a small patch stack on top of an upstream PocketBase baseline. The goal is to make upstream updates repeatable: rebase or replay the MySQL patches, run the same QA, and keep MySQL-specific seams easy to find.

## Patch Stack

Export the current MySQL patch stack:

```sh
scripts/export-mysql-patches.sh
```

By default the script exports commits after the upstream baseline tag. For the v0.39.4 upgrade, the base ref is `v0.39.4`:

```sh
node scripts/export-mysql-patches.mjs v0.39.4
```

For older branches based on v0.38.1, use:

```sh
node scripts/export-mysql-patches.mjs v0.38.1
```

Apply an exported stack to a clean worktree:

```sh
node scripts/apply-mysql-patches.mjs patches/mysql-poc
```

The apply script refuses to run on a dirty worktree (ignoring untracked files) and uses `git am --3way`, so patch metadata and commit boundaries remain intact.

## Upstream Update Checklist

1. Fetch upstream and choose the new PocketBase baseline commit or tag.
2. Create a new branch from that upstream baseline.
3. Apply the MySQL patch stack with `node scripts/apply-mysql-patches.mjs`.
4. Resolve conflicts one patch at a time, preserving commit boundaries when practical.
5. Run the SQLite baseline checks:

```sh
go build ./...
go test ./...
```

6. Run the MySQL PoC runtime check:

```sh
node scripts/mysql-runtime-qa.mjs
```

7. Update `docs/mysql-gap-analysis.md` with any new upstream drift, blocker, or verified behavior.
8. Export a fresh patch stack after the branch is clean:

```sh
node scripts/export-mysql-patches.mjs <new-base-ref>
```

## MySQL QA Script

`scripts/mysql-runtime-qa.mjs` starts a disposable MySQL 8.4 container (or uses an existing server with `--skip-docker`), boots PocketBase with `PB_DATABASE_DRIVER=mysql`, creates a superuser, exercises collection/record CRUD, checks sorted record listing, checks a text `LIKE` filter, runs the schema update matrix for select fields (rename, delete, single->multi, multi->single), verifies relation-many create/filter/expand, and verifies a simple view collection create/list/filter/update flow.

Requirements:

- Docker
- Go toolchain matching the repository
- `curl`
- `jq`

Useful environment overrides:

```sh
MYSQL_QA_IMAGE=mysql:8.4
MYSQL_QA_CONTAINER=pb-mysql-runtime-qa
MYSQL_QA_MYSQL_PORT=3307
MYSQL_QA_HTTP_ADDR=127.0.0.1:18090
MYSQL_QA_TMP_DIR=/tmp/opencode/pb-mysql-runtime-qa
MYSQL_QA_PASSWORD=pbpass
MYSQL_QA_DATABASE=pocketbase
```

The script is intentionally a smoke test, not a full compatibility suite. Extend it when a new runtime blocker is fixed so future upstream updates catch regressions.

## Build Output

`go build ./...` is a compile verification step only. It does not produce a single final application binary in the repository root.

To build the runnable fork binary in this repository, use:

```sh
make build
```

or directly:

```sh
go build -o pocketbase-mysql ./examples/base
```

This produces the executable at:

```sh
./pocketbase-mysql
```

Run it with:

```sh
./pocketbase-mysql serve
```

## Container Output

This fork also supports an app-only container image build.

Local image build:

```sh
docker build -t pocketbase-mysql:local .
```

Example local stack:

```sh
docker compose -f docker-compose.mysql.yml up --build
```

GitHub container publishing:

- `.github/workflows/publish-ghcr.yaml`
- publishes to `ghcr.io/fadlee/pocketbase-mysql`
- branch push on `mysql/main` publishes the rolling branch image
- tag push `v*` publishes versioned image tags

## Refactor Boundaries

Keep MySQL-specific code behind small seams where possible:

- `core/db_dialect.go`: data DB driver detection and environment constants.
- `core/db_connect.go`: MySQL data DB routing; auxiliary DB remains SQLite for the PoC.
- `core/db_table.go`: data DB table, column, and index metadata lookups.
- `core/collection_validate.go`: index-name validation metadata lookup.
- `core/collection_record_table_sync.go` and `core/base.go`: SQLite-only maintenance such as `PRAGMA optimize`.
- `tools/search/filter.go` plus `core/record_field_resolver.go`: SQL fragments that need dialect-specific escaping.

Do not broaden the abstraction prematurely. Migration branching, field DDL differences, and partial-index normalization are still PoC seams and should be moved only when the relevant behavior has enough QA coverage.
