package cli

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// `every schema [command]` prints the JSON shape a command emits.
//
// Generated from the Go types by reflection rather than written by hand, for
// one reason: a hand-written schema goes stale the first time somebody adds a
// field, and nothing fails. This cannot drift, because there is nothing to
// keep in sync -- the types ARE the schema.
//
// Discoverable from the binary rather than a website, so a program can learn
// the contract from the thing it is already running.

// schemaFor maps a command to the type its --json output produces.
var schemaFor = map[string]any{
	"list":    []jsonRecord{},
	"inspect": TaskView{},
	"set":     TaskView{},
	"add":     TaskView{},
	"log":     logPayload{},
	"run":     runPayload{},
	"rm":      okPayload{},
	"pause":   okPayload{},
	"resume":  okPayload{},
	"doctor":  doctorPayload{},
	"version": versionPayload{},
	"error":   errorPayload{},
}

func (c *CLI) schema(args []string) error {
	args, _ = stripJSONFlag(args)

	if len(args) == 0 {
		out := map[string]any{}
		for name, v := range schemaFor {
			out[name] = describe(reflect.TypeOf(v))
		}
		return emitJSON(c.Stdout, out)
	}

	name := args[0]
	v, ok := schemaFor[name]
	if !ok {
		names := make([]string, 0, len(schemaFor))
		for n := range schemaFor {
			names = append(names, n)
		}
		sort.Strings(names)
		return coded(CodeUsage, "", "no schema for %s (have: %s)",
			rubyInspect(name), strings.Join(names, ", "))
	}
	return emitJSON(c.Stdout, describe(reflect.TypeOf(v)))
}

// describe renders a Go type as a JSON Schema fragment.
//
// Deliberately a small subset -- object, array, string, integer, number,
// boolean, and null for anything optional. Enough to tell a consumer what
// fields exist and what they hold, without a dependency and without pretending
// to cover a spec nothing here uses.
func describe(t reflect.Type) map[string]any {
	switch t.Kind() {
	case reflect.Pointer:
		// A pointer field is the one that can be null, which is exactly what a
		// consumer needs to know before dereferencing it.
		inner := describe(t.Elem())
		return map[string]any{"type": []string{typeName(inner), "null"}, "properties": inner["properties"]}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": describe(t.Elem())}
	case reflect.Struct:
		props := map[string]any{}
		var required []string
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			tag := f.Tag.Get("json")
			if tag == "-" || tag == "" {
				continue
			}
			parts := strings.Split(tag, ",")
			key := parts[0]
			optional := false
			for _, opt := range parts[1:] {
				if opt == "omitempty" {
					optional = true
				}
			}
			props[key] = describe(f.Type)
			if !optional && f.Type.Kind() != reflect.Pointer {
				required = append(required, key)
			}
		}
		sort.Strings(required)
		out := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			out["required"] = required
		}
		return out
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	default:
		return map[string]any{}
	}
}

func typeName(m map[string]any) string {
	if s, ok := m["type"].(string); ok {
		return s
	}
	return "object"
}

var _ = fmt.Sprintf
