package cpanelplugin

import _ "embed"

// indexHTML is the cPanel end-user status page. Served for any non-/api request in the
// user CGI; fetches ?api=summary, which the CGI proxies to the broker.
//
//go:embed assets/index.html
var indexHTML []byte

// whmHTML is the WHM admin relay-installer UI. Served by the privileged WHM CGI; drives
// the ?api=status|enable|disable|test|dns actions.
//
//go:embed assets/whm.html
var whmHTML []byte
