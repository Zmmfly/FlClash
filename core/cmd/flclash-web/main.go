//go:build webpanel && !android

package main

import (
	"core"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

//go:embed all:web_dist
var embeddedWebDist embed.FS

var Version = "dev"

func defaultDataDir(getenv func(string) string) string {
	if value := getenv("FLCLASH_DATA_DIR"); value != "" {
		return value
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "share", "flclash-web")
	}
	return "./flclash-data"
}

func parseOptions(args []string, getenv func(string) string, output io.Writer) (core.WebServerOptions, bool, error) {
	opts := core.WebServerOptions{ListenAddr: "127.0.0.1:8080", DataDir: defaultDataDir(getenv), WebDir: getenv("FLCLASH_WEB_DIR"), Username: getenv("FLCLASH_WEB_USERNAME"), Password: getenv("FLCLASH_WEB_PASSWORD"), PasswordFile: getenv("FLCLASH_WEB_PASSWORD_FILE"), AuthFile: getenv("FLCLASH_WEB_AUTH_FILE")}
	if value := getenv("FLCLASH_WEB_LISTEN"); value != "" {
		opts.ListenAddr = value
	}
	if value := getenv("FLCLASH_WEB_SECURE_COOKIE"); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return opts, false, errors.New("invalid FLCLASH_WEB_SECURE_COOKIE boolean")
		}
		opts.SecureCookie = parsed
	}
	flags := flag.NewFlagSet("flclash-web", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&opts.ListenAddr, "listen", opts.ListenAddr, "HTTP/WebSocket listen address (FLCLASH_WEB_LISTEN)")
	flags.StringVar(&opts.DataDir, "data-dir", opts.DataDir, "Persistent data directory (FLCLASH_DATA_DIR)")
	flags.StringVar(&opts.WebDir, "web-dir", opts.WebDir, "Optional Flutter Web asset directory (FLCLASH_WEB_DIR)")
	flags.StringVar(&opts.Username, "username", opts.Username, "Bootstrap login username when the credential store does not exist (FLCLASH_WEB_USERNAME)")
	flags.StringVar(&opts.PasswordFile, "password-file", opts.PasswordFile, "Bootstrap password file outside data/assets (FLCLASH_WEB_PASSWORD_FILE); exclusive with FLCLASH_WEB_PASSWORD during initialization")
	flags.StringVar(&opts.AuthFile, "auth-file", opts.AuthFile, "Persistent credential file outside data/assets (FLCLASH_WEB_AUTH_FILE); default <absolute data-dir>.auth.json")
	flags.BoolVar(&opts.SecureCookie, "secure-cookie", opts.SecureCookie, "Require HTTPS browser origin and Secure cookies behind a Host-preserving reverse proxy")
	version := flags.Bool("version", false, "Print version and exit")
	if err := flags.Parse(args); err != nil {
		return opts, false, err
	}
	if flags.NArg() > 0 {
		return opts, false, errors.New("unexpected positional arguments")
	}
	if *version {
		return opts, true, nil
	}
	assets, err := fs.Sub(embeddedWebDist, "web_dist")
	if err != nil {
		return opts, false, errors.New("cannot load embedded assets")
	}
	opts.EmbeddedFS = assets
	return opts, false, nil
}

func main() {
	opts, version, err := parseOptions(os.Args[1:], os.Getenv, os.Stderr)
	if err == nil && version {
		fmt.Printf("FlClash-web %s\n", Version)
		return
	}
	if err == nil {
		err = core.StartWebServer(opts)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "FlClash-web: %v\n", err)
		os.Exit(1)
	}
}
