// Package jsonkeys finds the keys of a JSON document that a Go type has no
// field for. encoding/json drops them without a word, so a misspelt or
// unsupported setting in gc.json or a theater file silently does nothing;
// this is how the GC says so at startup.
package jsonkeys

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
)

// Unknown lists, as dotted paths, every key in raw that v's type does not
// decode. Keys starting with "//" or "_" are comments and are skipped.
func Unknown(raw []byte, v any) []string {
	var doc any
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	var out []string
	walk(doc, reflect.TypeOf(v), "", &out)
	sort.Strings(out)
	return out
}

func walk(doc any, t reflect.Type, path string, out *[]string) {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return
	}
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := doc.(map[string]any)
		if !ok {
			return
		}
		fields := fieldsOf(t)
		for k, val := range obj {
			if strings.HasPrefix(k, "//") || strings.HasPrefix(k, "_") {
				continue
			}
			ft, ok := fields[strings.ToLower(k)]
			if !ok {
				*out = append(*out, join(path, k))
				continue
			}
			walk(val, ft, join(path, k), out)
		}
	case reflect.Map:
		obj, ok := doc.(map[string]any)
		if !ok {
			return
		}
		for k, val := range obj {
			walk(val, t.Elem(), join(path, k), out)
		}
	case reflect.Slice, reflect.Array:
		arr, ok := doc.([]any)
		if !ok {
			return
		}
		for i, val := range arr {
			walk(val, t.Elem(), path+"["+itoa(i)+"]", out)
		}
	}
}

// fieldsOf maps a struct's JSON names (lower-cased, as encoding/json matches
// them) to their types, including embedded structs' fields.
func fieldsOf(t reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				for k, v := range fieldsOf(ft) {
					out[k] = v
				}
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[strings.ToLower(name)] = f.Type
	}
	return out
}

func join(path, k string) string {
	if path == "" {
		return k
	}
	return path + "." + k
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	return string(b[n:])
}
