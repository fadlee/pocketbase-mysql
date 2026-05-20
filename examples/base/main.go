package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/plugins/ghupdate"
	"github.com/pocketbase/pocketbase/plugins/jsvm"
	"github.com/pocketbase/pocketbase/plugins/migratecmd"
	"github.com/pocketbase/pocketbase/tools/hook"
	"github.com/pocketbase/pocketbase/tools/osutils"
)

func main() {
	app := pocketbase.New()

	// ---------------------------------------------------------------
	// Optional plugin flags:
	// ---------------------------------------------------------------

	var hooksDir string
	app.RootCmd.PersistentFlags().StringVar(
		&hooksDir,
		"hooksDir",
		"",
		"the directory with the JS app hooks",
	)

	var hooksWatch bool
	app.RootCmd.PersistentFlags().BoolVar(
		&hooksWatch,
		"hooksWatch",
		true,
		"auto restart the app on pb_hooks file change; it has no effect on Windows",
	)

	var hooksPool int
	app.RootCmd.PersistentFlags().IntVar(
		&hooksPool,
		"hooksPool",
		15,
		"the total prewarm goja.Runtime instances for the JS app hooks execution",
	)

	var migrationsDir string
	app.RootCmd.PersistentFlags().StringVar(
		&migrationsDir,
		"migrationsDir",
		"",
		"the directory with the user defined migrations",
	)

	var automigrate bool
	app.RootCmd.PersistentFlags().BoolVar(
		&automigrate,
		"automigrate",
		true,
		"enable/disable auto migrations",
	)

	var publicDir string
	app.RootCmd.PersistentFlags().StringVar(
		&publicDir,
		"publicDir",
		defaultPublicDir(),
		"the directory to serve static files",
	)

	var indexFallback bool
	app.RootCmd.PersistentFlags().BoolVar(
		&indexFallback,
		"indexFallback",
		true,
		"fallback the request to index.html on missing static path, e.g. when pretty urls are used with SPA",
	)

	// ---------------------------------------------------------------
	// MySQL database flags:
	// ---------------------------------------------------------------

	var dbDriver string
	app.RootCmd.PersistentFlags().StringVar(
		&dbDriver,
		"db-driver",
		"",
		"database driver to use (default: sqlite; use 'mysql' for MySQL/MariaDB)",
	)

	var dbHost string
	app.RootCmd.PersistentFlags().StringVar(
		&dbHost,
		"db-host",
		"127.0.0.1",
		"MySQL host (used when --db-driver=mysql)",
	)

	var dbPort int
	app.RootCmd.PersistentFlags().IntVar(
		&dbPort,
		"db-port",
		3306,
		"MySQL port (used when --db-driver=mysql)",
	)

	var dbUser string
	app.RootCmd.PersistentFlags().StringVar(
		&dbUser,
		"db-user",
		"root",
		"MySQL user (used when --db-driver=mysql)",
	)

	var dbPassword string
	app.RootCmd.PersistentFlags().StringVar(
		&dbPassword,
		"db-password",
		"",
		"MySQL password (used when --db-driver=mysql)",
	)

	var dbName string
	app.RootCmd.PersistentFlags().StringVar(
		&dbName,
		"db-name",
		"pocketbase",
		"MySQL database name (used when --db-driver=mysql)",
	)

	app.RootCmd.ParseFlags(os.Args[1:])

	// Apply MySQL flags: CLI flags take priority over env vars.
	// Only override env vars if --db-driver flag was explicitly provided.
	if dbDriver != "" {
		os.Setenv("PB_DATABASE_DRIVER", dbDriver)
		if dbDriver == "mysql" {
			creds := dbUser
			if dbPassword != "" {
				creds += ":" + dbPassword
			}
			dsn := fmt.Sprintf("%s@tcp(%s:%d)/%s?parseTime=true&multiStatements=true",
				creds, dbHost, dbPort, dbName)
			os.Setenv("PB_DATABASE_DSN", dsn)
		}
	}

	// ---------------------------------------------------------------
	// Plugins and hooks:
	// ---------------------------------------------------------------

	// load jsvm (pb_hooks and pb_migrations)
	jsvm.MustRegister(app, jsvm.Config{
		MigrationsDir: migrationsDir,
		HooksDir:      hooksDir,
		HooksWatch:    hooksWatch,
		HooksPoolSize: hooksPool,
	})

	// migrate command (with js templates)
	migratecmd.MustRegister(app, app.RootCmd, migratecmd.Config{
		TemplateLang: migratecmd.TemplateLangJS,
		Automigrate:  automigrate,
		Dir:          migrationsDir,
	})

	// GitHub selfupdate
	ghupdate.MustRegister(app, app.RootCmd, ghupdate.Config{
		Owner:             "fadlee",
		Repo:              "pocketbase-mysql",
		ArchiveExecutable: "pocketbase-mysql",
	})

	// static route to serves files from the provided public dir
	// (if publicDir exists and the route path is not already defined)
	app.OnServe().Bind(&hook.Handler[*core.ServeEvent]{
		Func: func(e *core.ServeEvent) error {
			if !e.Router.HasRoute(http.MethodGet, "/{path...}") {
				e.Router.GET("/{path...}", apis.Static(os.DirFS(publicDir), indexFallback))
			}

			return e.Next()
		},
		Priority: 999, // execute as latest as possible to allow users to provide their own route
	})

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}

// the default pb_public dir location is relative to the executable
func defaultPublicDir() string {
	if osutils.IsProbablyGoRun() {
		return "./pb_public"
	}

	return filepath.Join(os.Args[0], "../pb_public")
}
