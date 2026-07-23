// Command cpanel-plugin is the MX Sentinel cPanel/WHM plugin. One binary, two modes:
//
//	mxsentinel-plugin serve    Run the root-owned broker daemon (holds the API token,
//	                           listens on a unix socket, scopes responses by peer uid).
//	mxsentinel-plugin cgi      Run as a CGI under cpsrvd (serves the dashboard and
//	                           proxies /api requests to the broker). Also auto-selected
//	                           when invoked with the CGI environment set.
//
// See docs/cpanel-plugin.md and plugins/cpanel/ for installation.
package main

import (
	"fmt"
	"os"

	"github.com/zezekim/mxsentinel/internal/cpanelplugin"
)

func main() {
	mode := ""
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}

	switch {
	case mode == "serve":
		if err := cpanelplugin.RunDaemon(os.Getenv("MXS_PLUGIN_CONFIG")); err != nil {
			fmt.Fprintln(os.Stderr, "cpanel-plugin serve:", err)
			os.Exit(1)
		}
	case mode == "cgi" || os.Getenv("GATEWAY_INTERFACE") != "":
		if err := cpanelplugin.RunCGI(""); err != nil {
			fmt.Fprintln(os.Stderr, "cpanel-plugin cgi:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: mxsentinel-plugin {serve|cgi}")
		os.Exit(2)
	}
}
