<p align="center">
    <a href="https://pocketbase.io" target="_blank" rel="noopener">
        <img src="https://i.imgur.com/aCBbjKx.png" alt="PocketBase - open source backend in 1 file" />
    </a>
</p>

<p align="center">
    <a href="https://github.com/pocketbase/pocketbase/actions/workflows/release.yaml" target="_blank" rel="noopener"><img src="https://github.com/pocketbase/pocketbase/actions/workflows/release.yaml/badge.svg" alt="build" /></a>
    <a href="https://github.com/pocketbase/pocketbase/releases" target="_blank" rel="noopener"><img src="https://img.shields.io/github/release/pocketbase/pocketbase.svg" alt="Latest releases" /></a>
    <a href="https://pkg.go.dev/github.com/pocketbase/pocketbase" target="_blank" rel="noopener"><img src="https://godoc.org/github.com/pocketbase/pocketbase?status.svg" alt="Go package documentation" /></a>
</p>

## MySQL Fork Status

This repository is a PocketBase fork that adds a MySQL-backed data DB PoC while keeping the upstream SQLite-first behavior as the default path.

Current baseline:

- upstream source imported from PocketBase `v0.38.0`
- active development branch: `mysql/main`
- upstream remote kept as `upstream`
- GitHub fork repo: `fadlee/pocketbase-mysql`

Important scope notes:

- This is not a drop-in replacement for upstream PocketBase releases yet.
- The fork currently targets verified MySQL runtime compatibility for the covered features below.
- Auxiliary/log DB still follows the existing SQLite path during the PoC.

## Branch Strategy

Recommended branch layout for this fork:

- `upstream/master` or upstream tags remain the source baseline.
- `mysql/main` is the long-lived integration branch for the MySQL fork.
- short-lived feature/fix branches branch off `mysql/main` and merge back there.
- do **not** merge this work into local `main`; keep `main` disposable or unused if it does not track the fork history you want to publish.

Recommended update flow:

1. fetch new upstream tags/commits
2. rebase or replay the MySQL patch stack onto the new upstream baseline
3. run `go test ./...`
4. run `scripts/mysql-runtime-qa.sh`
5. export a fresh patch stack if needed

See `docs/mysql-upstream-workflow.md` for the detailed rebase and patch workflow.

## Parity Snapshot

Compared with upstream PocketBase `v0.38.0`, the MySQL fork currently has the following runtime status:

| Area | Status | Notes |
|---|---|---|
| Server boot on MySQL data DB | Working | Fresh MySQL 8.4 container verified |
| Collections CRUD basics | Working | Create/list/update core flows covered |
| Record CRUD basics | Working | Create/list/filter basic records covered |
| Schema update matrix | Working | Rename, delete, single->multi, multi->single verified |
| Select fields | Working | Single and multiple select verified |
| Relation-many runtime | Working | Create, filter, expand verified for covered cases |
| Simple view collections | Working | Create, list, filter, base-record update visibility verified |
| GitHub binary CI artifacts | Working | Branch/PR workflow uploads artifacts |
| GitHub release binaries | Working on tagged releases | Triggered by pushing tags matching `v*` |
| Full upstream feature parity | Not claimed | This fork is still a MySQL compatibility PoC, not full parity |

For the detailed change log and blockers/fixes history, see `docs/mysql-gap-analysis.md`.

## Build and Release

`go build ./...` only checks compilation. To build the runnable fork binary:

```sh
go build -o pocketbase-mysql ./examples/base
```

This produces:

```sh
./pocketbase-mysql
```

You can run it with:

```sh
./pocketbase-mysql serve
```

CI behavior in this fork:

- branch/PR pushes run `.github/workflows/build-pocketbase-mysql.yaml` and upload build artifacts to the Actions run
- tag pushes matching `v*` run `.github/workflows/release-pocketbase-mysql.yaml` and attach binaries to GitHub Releases

## Container Image

This fork uses an app-only container image. It does **not** extend the official MySQL image.

Why:

- PocketBase and MySQL should remain separate services.
- the app can connect to managed MySQL or any external MySQL deployment.
- container lifecycle stays simple: one container, one primary process.

Files:

- `Dockerfile` - multi-stage build for `pocketbase-mysql`
- `.dockerignore` - keeps the build context small
- `docker-compose.mysql.yml` - example app + MySQL composition

Build locally:

```sh
docker build -t pocketbase-mysql:local .
```

Run the image directly:

```sh
docker run --rm -p 8090:8090 \
  -e PB_DATABASE_DRIVER=mysql \
  -e PB_DATABASE_DSN='root:pbpass@tcp(host.docker.internal:3306)/pocketbase?parseTime=true&multiStatements=true' \
  pocketbase-mysql:local
```

Run with the example compose stack:

```sh
docker compose -f docker-compose.mysql.yml up --build
```

The app container stores PocketBase runtime data in `/pb_data`.

## GHCR Publishing

This fork now publishes an OCI image to GHCR via `.github/workflows/publish-ghcr.yaml`.

Publish behavior:

- push to `mysql/main` -> push `ghcr.io/fadlee/pocketbase-mysql:mysql-main`
- default branch builds can also carry `latest` when `mysql/main` is the repo default branch
- push tag `v*` -> push tag-matched image tags, e.g. `ghcr.io/fadlee/pocketbase-mysql:v0.38.0-mysql.1`

The workflow builds multi-arch images for:

- `linux/amd64`
- `linux/arm64`

Example release flow:

```sh
git tag v0.1.0
git push origin v0.1.0
```

After the tag push completes, release binaries should appear in the GitHub Releases page for this fork.

[PocketBase](https://pocketbase.io) is an open source Go backend that includes:

- embedded database (_SQLite_) with **realtime subscriptions**
- built-in **files and users management**
- convenient **Admin dashboard UI**
- and simple **REST-ish API**

**For documentation and examples, please visit https://pocketbase.io/docs.**

> [!WARNING]
> Please keep in mind that PocketBase is still under active development
> and therefore full backward compatibility is not guaranteed before reaching v1.0.0.

## API SDK clients

The easiest way to interact with the PocketBase Web APIs is to use one of the official SDK clients:

- **JavaScript - [pocketbase/js-sdk](https://github.com/pocketbase/js-sdk)** (_Browser, Node.js, React Native_)
- **Dart - [pocketbase/dart-sdk](https://github.com/pocketbase/dart-sdk)** (_Web, Mobile, Desktop, CLI_)

You could also check the recommendations in https://pocketbase.io/docs/how-to-use/.


## Overview

### Use as standalone app

You could download the prebuilt executable for your platform from the [Releases page](https://github.com/pocketbase/pocketbase/releases).
Once downloaded, extract the archive and run `./pocketbase serve` in the extracted directory.

The prebuilt executables are based on the [`examples/base/main.go` file](https://github.com/pocketbase/pocketbase/blob/master/examples/base/main.go) and comes with the JS VM plugin enabled by default which allows to extend PocketBase with JavaScript (_for more details please refer to [Extend with JavaScript](https://pocketbase.io/docs/js-overview/)_).

### Use as a Go framework/toolkit

PocketBase is distributed as a regular Go library package which allows you to build
your own custom app specific business logic and still have a single portable executable at the end.

Here is a minimal example:

0. [Install Go 1.25+](https://go.dev/doc/install) (_if you haven't already_)

1. Create a new project directory with the following `main.go` file inside it:
    ```go
    package main

    import (
        "log"

        "github.com/pocketbase/pocketbase"
        "github.com/pocketbase/pocketbase/core"
    )

    func main() {
        app := pocketbase.New()

        app.OnServe().BindFunc(func(se *core.ServeEvent) error {
            // registers new "GET /hello" route
            se.Router.GET("/hello", func(re *core.RequestEvent) error {
                return re.String(200, "Hello world!")
            })

            return se.Next()
        })

        if err := app.Start(); err != nil {
            log.Fatal(err)
        }
    }
    ```

2. To init the dependencies, run `go mod init myapp && go mod tidy`.

3. To start the application, run `go run main.go serve`.

4. To build a statically linked executable, you can run `CGO_ENABLED=0 go build` and then start the created executable with `./myapp serve`.

_For more details please refer to [Extend with Go](https://pocketbase.io/docs/go-overview/)._

### Building and running the repo main.go example

To build the minimal standalone executable, like the prebuilt ones in the releases page, you can simply run `go build` inside the `examples/base` directory:

0. [Install Go 1.25+](https://go.dev/doc/install) (_if you haven't already_)
1. Clone/download the repo
2. Navigate to `examples/base`
3. Run `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build`
   (_https://go.dev/doc/install/source#environment_)
4. Start the created executable by running `./base serve`.

Note that the supported build targets by the pure Go SQLite driver at the moment are:

```
darwin  amd64
darwin  arm64
freebsd amd64
freebsd arm64
linux   386
linux   amd64
linux   arm
linux   arm64
linux   loong64
linux   ppc64le
linux   riscv64
linux   s390x
windows 386
windows amd64
windows arm64
```

### Testing

PocketBase comes with mixed bag of unit and integration tests.
To run them, use the standard `go test` command:

```sh
go test ./...
```

Check also the [Testing guide](http://pocketbase.io/docs/testing) to learn how to write your own custom application tests.

## Security

If you discover a security vulnerability within PocketBase, please send an e-mail to **support at pocketbase.io**.

All reports will be promptly addressed and you'll be credited in the fix release notes.

## Contributing

PocketBase is free and open source project licensed under the [MIT License](LICENSE.md).
You are free to do whatever you want with it, even offering it as a paid service.

You could help continuing its development by:

- [Contribute to the source code](CONTRIBUTING.md)
- [Suggest new features and report issues](https://github.com/pocketbase/pocketbase/issues)

Please refrain creating PRs for _new features_ without previously discussing the implementation details.
PocketBase has a [roadmap](https://github.com/orgs/pocketbase/projects/2) and I try to work on issues in specific order and such PRs often come in out of nowhere and skew all initial planning with tedious back-and-forth communication.

Don't get upset if I close your PR, even if it is well executed and tested. This doesn't mean that it will never be merged.
Later we can always refer to it and/or take pieces of your implementation when the time comes to work on the issue (don't worry you'll be credited in the release notes).

> [!IMPORTANT]
> Due to recent LLM spam, PRs are temporary disabled and only existing collaborators can open a PR.
> If you stumble on a problem that you want to fix, please consider instead opening an issue or discussion with link to your fork _(if not obvious - LLM contributions are not welcome)_.
> This status may change in the future in case GitHub finally decide to do something about the constant spam, or when I find time to move the project somewhere else.
