// Package nlquery implements MX Sentinel's natural-language analytics ("ask your
// mail logs"): a user asks a question in plain English and a local, OpenAI-compatible
// LLM answers it — WITHOUT ever seeing message bodies or subject lines.
//
// Privacy design (enforced in code, not just docs):
//
//   - The model NEVER receives raw mail content. It plans over a fixed WHITELIST of
//     safe, parameterized AGGREGATE queries (see registry.go). It cannot emit free-form
//     SQL; it may only pick a whitelisted tool + validated arguments.
//   - We execute the chosen tool against ClickHouse/Postgres, feed the AGGREGATE ROWS
//     back to the model, and it composes a natural-language answer.
//   - No tool exposes, and no arg accepts, a message body or subject field. A unit test
//     (registry_test.go) asserts this invariant.
//
// The prompt building, plan parsing and argument validation are pure and unit-tested;
// the LLM is reached through an interface (ai.Client) so tests need no live endpoint.
package nlquery

import (
	"os"
	"strconv"
	"strings"
)

// Config points the NL-analytics layer at the same local OpenAI-compatible endpoint the
// rest of the AI layer uses (config.AIConfig), plus a couple of NL-specific knobs. It is
// loaded from the environment at request time (mirrors internal/rbl.LoadConfig) so the API
// server does not need to carry it on the Server struct.
type Config struct {
	Endpoint    string // MXS_AI_ENDPOINT     — base URL, e.g. http://localhost:11434/v1
	Model       string // MXS_AI_MODEL
	APIKey      string // MXS_AI_APIKEY       — optional; many local servers ignore it
	TimeoutSecs int    // MXS_AI_TIMEOUTSECS
	MaxTools    int    // MXS_NLQUERY_MAX_TOOLS — cap on whitelisted queries per question
}

// Defaults mirror config.Defaults() for the AI block so behaviour matches the rest of the
// platform when the env vars are unset.
func defaultConfig() Config {
	return Config{
		Endpoint:    "http://localhost:11434/v1",
		Model:       "llama3",
		TimeoutSecs: 60,
		MaxTools:    3,
	}
}

// LoadConfig reads the NL-analytics configuration from MXS_* environment variables,
// falling back to defaults. It intentionally reuses the AI endpoint/model/key so operators
// configure the LLM in exactly one place.
func LoadConfig() Config {
	c := defaultConfig()
	if v := strings.TrimSpace(os.Getenv("MXS_AI_ENDPOINT")); v != "" {
		c.Endpoint = v
	}
	if v := strings.TrimSpace(os.Getenv("MXS_AI_MODEL")); v != "" {
		c.Model = v
	}
	if v := strings.TrimSpace(os.Getenv("MXS_AI_APIKEY")); v != "" {
		c.APIKey = v
	}
	if v := strings.TrimSpace(os.Getenv("MXS_AI_TIMEOUTSECS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.TimeoutSecs = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("MXS_NLQUERY_MAX_TOOLS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.MaxTools = n
		}
	}
	return c
}
