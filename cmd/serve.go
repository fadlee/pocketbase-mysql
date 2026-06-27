package cmd

import (
	"errors"
	"net/http"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/spf13/cobra"
)

// NewServeCommand creates and returns new command responsible for
// starting the default PocketBase web server.
func NewServeCommand(app core.App, showStartBanner bool) *cobra.Command {
	var allowedOrigins []string
	var httpAddr string
	var httpsAddr string

	command := &cobra.Command{
		Use:   "serve [domain(s)]",
		Args:  cobra.ArbitraryArgs,
		Short: "Starts the web server (default to 127.0.0.1:8090 if no domain is specified)",
		Long: `Starts the web server (default to 127.0.0.1:8090 if no domain is specified).

MySQL fork usage:
  SQLite remains the default data DB. To use MySQL for the data DB, set:
    PB_DATABASE_DRIVER=mysql
    PB_DATABASE_DSN=<mysql-dsn>

  Example DSN:
    user:pass@tcp(127.0.0.1:3306)/pocketbase?parseTime=true&multiStatements=true

  Linux/macOS/Git Bash:
    PB_DATABASE_DRIVER=mysql \
    PB_DATABASE_DSN='user:pass@tcp(127.0.0.1:3306)/pocketbase?parseTime=true&multiStatements=true' \
    pocketbase-mysql serve

  PowerShell:
    $env:PB_DATABASE_DRIVER = "mysql"
    $env:PB_DATABASE_DSN = "user:pass@tcp(127.0.0.1:3306)/pocketbase?parseTime=true&multiStatements=true"
    .\pocketbase-mysql.exe serve

  CMD.exe:
    set PB_DATABASE_DRIVER=mysql
    set PB_DATABASE_DSN=user:pass@tcp(127.0.0.1:3306)/pocketbase?parseTime=true^&multiStatements=true
    pocketbase-mysql.exe serve

  DSN notes:
    parseTime=true        parse MySQL DATE/DATETIME/TIMESTAMP values as Go time values.
    multiStatements=true  allow migration/schema SQL that contains multiple statements.

  MySQL support:
    Verified with MySQL 8.4 via the runtime QA suite. Other MySQL 8.x versions may
    work but are not guaranteed by this fork.

  Fork scope:
    Only the data DB is routed to MySQL. The auxiliary/log DB remains SQLite.
    Full upstream PocketBase feature parity is not claimed.`,
		SilenceUsage: true,
		RunE: func(command *cobra.Command, args []string) error {
			// set default listener addresses if at least one domain is specified
			if len(args) > 0 {
				if httpAddr == "" {
					httpAddr = "0.0.0.0:80"
				}
				if httpsAddr == "" {
					httpsAddr = "0.0.0.0:443"
				}
			} else {
				if httpAddr == "" {
					httpAddr = "127.0.0.1:8090"
				}
			}

			err := apis.Serve(app, apis.ServeConfig{
				HttpAddr:           httpAddr,
				HttpsAddr:          httpsAddr,
				ShowStartBanner:    showStartBanner,
				AllowedOrigins:     allowedOrigins,
				CertificateDomains: args,
			})

			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}

			return err
		},
	}

	command.PersistentFlags().StringSliceVar(
		&allowedOrigins,
		"origins",
		[]string{"*"},
		"CORS allowed domain origins list",
	)

	command.PersistentFlags().StringVar(
		&httpAddr,
		"http",
		"",
		"TCP address to listen for the HTTP server\n(if domain args are specified - default to 0.0.0.0:80, otherwise - default to 127.0.0.1:8090)",
	)

	command.PersistentFlags().StringVar(
		&httpsAddr,
		"https",
		"",
		"TCP address to listen for the HTTPS server\n(if domain args are specified - default to 0.0.0.0:443, otherwise - default to empty string, aka. no TLS)\nThe incoming HTTP traffic also will be auto redirected to the HTTPS version",
	)

	return command
}
