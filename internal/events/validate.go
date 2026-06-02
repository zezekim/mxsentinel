package events

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/zezekim/mxsentinel/schemas"
)

// Validator validates event envelopes against the embedded JSON Schemas. The schemas
// cross-reference each other by their canonical $id, so every schema is registered in
// one compiler before any is compiled (see docs/event-contracts.md).
type Validator struct {
	// byFamily maps an event family ("smtp", "dns", ...) to its compiled schema.
	byFamily map[string]*jsonschema.Schema
}

// NewValidator loads schemas/events/*.json, registers them all by $id, and compiles the
// per-family event schemas.
func NewValidator() (*Validator, error) {
	c := jsonschema.NewCompiler()

	entries, err := fs.ReadDir(schemas.Events, "events")
	if err != nil {
		return nil, fmt.Errorf("read embedded schemas: %w", err)
	}

	// First pass: register every schema document under its $id so $ref resolves offline.
	ids := map[string]string{} // family -> $id, for the schemas we compile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := schemas.Events.ReadFile(path.Join("events", e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		id, ok := doc.(map[string]any)["$id"].(string)
		if !ok || id == "" {
			return nil, fmt.Errorf("%s: missing $id", e.Name())
		}
		if err := c.AddResource(id, doc); err != nil {
			return nil, fmt.Errorf("add resource %s: %w", e.Name(), err)
		}

		// stem: "smtp_event.schema" -> family "smtp"; skip the envelope.
		stem := strings.TrimSuffix(e.Name(), ".json")
		stem = strings.TrimSuffix(stem, ".schema")
		if stem == "envelope" {
			continue
		}
		family := strings.TrimSuffix(stem, "_event")
		ids[family] = id
	}

	// Second pass: compile each family's schema (envelope $ref now resolvable).
	v := &Validator{byFamily: make(map[string]*jsonschema.Schema, len(ids))}
	for family, id := range ids {
		sch, err := c.Compile(id)
		if err != nil {
			return nil, fmt.Errorf("compile %s schema: %w", family, err)
		}
		v.byFamily[family] = sch
	}
	if len(v.byFamily) == 0 {
		return nil, fmt.Errorf("no event schemas were compiled")
	}
	return v, nil
}

// Families returns the event families this validator knows about (test/debug helper).
func (v *Validator) Families() []string {
	out := make([]string, 0, len(v.byFamily))
	for f := range v.byFamily {
		out = append(out, f)
	}
	return out
}

// ValidateInstance validates an already-parsed JSON instance for the given event type.
func (v *Validator) ValidateInstance(eventType string, instance any) error {
	fam := family(eventType)
	sch, ok := v.byFamily[fam]
	if !ok {
		return fmt.Errorf("no schema for event family %q (event_type=%q)", fam, eventType)
	}
	if err := sch.Validate(instance); err != nil {
		return fmt.Errorf("schema validation failed for %q: %w", eventType, err)
	}
	return nil
}

// ValidateRaw parses raw JSON and validates it for the given event type.
func (v *Validator) ValidateRaw(eventType string, raw []byte) error {
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("parse event json: %w", err)
	}
	return v.ValidateInstance(eventType, inst)
}

func family(eventType string) string {
	if i := strings.IndexByte(eventType, '.'); i > 0 {
		return eventType[:i]
	}
	return eventType
}
