// Package jsonstrict adds path-aware unknown-field checks, including structs
// with custom UnmarshalJSON methods. Maps and RawMessage remain open JSON.
package jsonstrict

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
)

func Decode(data []byte, dst any) error {
	if err := fields(data, reflect.TypeOf(dst), ""); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("trailing JSON input")
	}
	return nil
}
func fields(data []byte, t reflect.Type, path string) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == reflect.TypeFor[json.RawMessage]() || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil {
			return err
		}
		names := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			names[name] = f.Type
		}
		keys := make([]string, 0, len(object))
		for k := range object {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p := k
			if path != "" {
				p = path + "." + k
			}
			child, ok := names[k]
			if !ok {
				return fmt.Errorf("unknown field %q", p)
			}
			if err := fields(object[k], child, p); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		var rows []json.RawMessage
		if err := json.Unmarshal(data, &rows); err != nil {
			return err
		}
		for i, row := range rows {
			if err := fields(row, t.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case reflect.Map:
		if t.Elem().Kind() == reflect.Interface {
			return nil
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil {
			return err
		}
		keys := make([]string, 0, len(object))
		for k := range object {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := fields(object[k], t.Elem(), path+"."+k); err != nil {
				return err
			}
		}
	}
	return nil
}
