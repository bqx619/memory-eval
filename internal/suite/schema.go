package suite

import (
	"encoding/json"
	"github.com/invopop/jsonschema"
	"reflect"
)

// SchemaJSON shares the draft, arbitrary-JSON mapping and stable encoding across
// suite, benchmark and registry schemas without coupling their model packages.
func SchemaJSON(value any) ([]byte, error) {
	r := jsonschema.Reflector{Mapper: func(t reflect.Type) *jsonschema.Schema {
		if t == reflect.TypeFor[json.RawMessage]() {
			return &jsonschema.Schema{}
		}
		return nil
	}}
	b, err := json.MarshalIndent(r.Reflect(value), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
func Schema() ([]byte, error)             { return SchemaJSON(Suite{}) }
func BundleHeaderSchema() ([]byte, error) { return SchemaJSON(BundleHeader{}) }
func EpisodeSchema() ([]byte, error)      { return SchemaJSON(Episode{}) }
