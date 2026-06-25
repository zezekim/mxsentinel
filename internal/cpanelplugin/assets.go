package cpanelplugin

import _ "embed"

// indexHTML is the self-contained dashboard shell (markup + CSS + JS). It is served
// for any non-/api request and fetches its data from the same URL with ?api=summary,
// which the CGI proxies to the broker.
//
//go:embed assets/index.html
var indexHTML []byte
