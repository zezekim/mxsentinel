// Package schemas embeds the source-of-truth contracts (event JSON Schemas and the
// reference DDL) so Go services can load them at runtime without filesystem access.
//
// The event schemas here are the SAME files documented in docs/event-contracts.md and
// are the contract that internal/events validates against on publish.
package schemas

import "embed"

// Events holds the JSON Schema files for every event family (envelope + smtp/dns/
// reputation/ai). They cross-reference each other by their canonical $id, so a loader
// must register all of them in one resolver before compiling. See internal/events.
//
//go:embed events/*.json
var Events embed.FS

// DDL holds the human-readable reference schema definitions. The *runnable* migrations
// live under /migrations (derived from these); these remain the readable spec.
//
//go:embed postgres/*.sql clickhouse/*.sql
var DDL embed.FS
