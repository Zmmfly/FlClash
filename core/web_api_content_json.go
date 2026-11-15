//go:build webpanel && !android

package core

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
)

func webContentStrictJSON(data []byte, target any) error { return webAPIStrictJSON(data, target) }

func webContentMarshal(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return webContentNormalizeCollections(data, reflect.TypeOf(value))
}

func webContentNormalizeCollections(data []byte, t reflect.Type) ([]byte, error) {
	var value any
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := d.Decode(&value); err != nil {
		return nil, err
	}
	var walk func(any, reflect.Type) any
	walk = func(v any, t reflect.Type) any {
		if t == reflect.TypeFor[json.RawMessage]() {
			return v
		}
		if t.Kind() == reflect.Pointer {
			if v == nil {
				return nil
			}
			return walk(v, t.Elem())
		}
		switch t.Kind() {
		case reflect.Struct:
			object, ok := v.(map[string]any)
			if !ok {
				return v
			}
			fields := map[string]reflect.Type{}
			var collect func(reflect.Type)
			collect = func(st reflect.Type) {
				for i := 0; i < st.NumField(); i++ {
					f := st.Field(i)
					if !f.IsExported() {
						continue
					}
					name := strings.Split(f.Tag.Get("json"), ",")[0]
					if name == "-" {
						continue
					}
					if f.Anonymous && name == "" {
						collect(f.Type)
						continue
					}
					if name == "" {
						name = f.Name
					}
					fields[name] = f.Type
				}
			}
			collect(t)
			for k, x := range object {
				if ft, ok := fields[k]; ok {
					object[k] = walk(x, ft)
				}
			}
			return object
		case reflect.Map:
			if v == nil {
				return map[string]any{}
			}
			if object, ok := v.(map[string]any); ok {
				for k, x := range object {
					object[k] = walk(x, t.Elem())
				}
				return object
			}
		case reflect.Slice:
			if t.Elem().Kind() == reflect.Uint8 {
				return v
			}
			if v == nil {
				return []any{}
			}
			if list, ok := v.([]any); ok {
				for i, x := range list {
					list[i] = walk(x, t.Elem())
				}
				return list
			}
		}
		return v
	}
	return json.Marshal(walk(value, t))
}
