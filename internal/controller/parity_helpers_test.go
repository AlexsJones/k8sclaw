package controller

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
)

// Helpers shared by the two divergence suites:
//
//   - agentrun_backend_parity_test.go — Job vs Sandbox backend pod specs
//   - ensemble_parity_test.go         — Agent create vs update convergence
//
// Both ask the same question of different types: given two ways of producing a
// value that should agree, where do they differ? diffStructs answers it for any
// struct; fillStruct produces the fully-populated inputs that make an omission
// observable.

// nameKeyedCollections are the field names whose list elements are identified by
// a "name" key rather than by position. normalizeNode rewrites them into maps so
// comparison ignores ordering, and childPath renders them as parent[name].
var nameKeyedCollections = []string{
	"containers",
	"initContainers",
	"ephemeralContainers",
	"volumes",
	"volumeMounts",
	"env",
	"imagePullSecrets",
	"sources",
	"channels",
	"skills",
	"mcpServers",
}

// ── the differ ────────────────────────────────────────────────────────────────

// structDiff is one field-level difference between two values, as a dotted path
// plus each side's rendering. "a" and "b" are whatever the caller passed.
type structDiff struct {
	path string
	a    string
	b    string
}

// diffStructs compares two values of the same type field by field and returns each
// difference as a dotted path rooted at root.
//
// Both sides go through ToUnstructured first so representation differences (a
// quantity as "100m" vs a number, int32 vs int64) do not register as differences.
// Pass pointers — ToUnstructured requires them.
func diffStructs(t *testing.T, root string, a, b interface{}) []structDiff {
	t.Helper()

	aMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(a)
	if err != nil {
		t.Fatalf("normalise %s (a): %v", root, err)
	}
	bMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(b)
	if err != nil {
		t.Fatalf("normalise %s (b): %v", root, err)
	}

	var diffs []structDiff
	walkDiff(root, normalizeNode(aMap), normalizeNode(bMap), &diffs)
	sort.Slice(diffs, func(i, j int) bool { return diffs[i].path < diffs[j].path })
	return diffs
}

// normalizeNode rewrites name-keyed lists (containers, initContainers, volumes,
// env, volumeMounts, …) into maps keyed by name, so comparison is by name rather
// than position. Mutators append rather than insert.
func normalizeNode(node interface{}) interface{} {
	switch v := node.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(v))
		for k, val := range v {
			out[k] = normalizeNode(val)
		}
		return out
	case []interface{}:
		if keyed, ok := keyByName(v); ok {
			return keyed
		}
		out := make([]interface{}, len(v))
		for i, val := range v {
			out[i] = normalizeNode(val)
		}
		return out
	default:
		return node
	}
}

// keyByName converts a list to a name-keyed map when every element is a map with
// a unique string "name". Duplicate or missing names fall back to positional
// comparison.
func keyByName(list []interface{}) (map[string]interface{}, bool) {
	if len(list) == 0 {
		return nil, false
	}
	out := make(map[string]interface{}, len(list))
	for _, elem := range list {
		m, ok := elem.(map[string]interface{})
		if !ok {
			return nil, false
		}
		name, ok := m["name"].(string)
		if !ok || name == "" {
			return nil, false
		}
		if _, dup := out[name]; dup {
			return nil, false
		}
		out[name] = normalizeNode(m)
	}
	return out, true
}

func walkDiff(path string, aVal, bVal interface{}, diffs *[]structDiff) {
	switch av := aVal.(type) {
	case map[string]interface{}:
		bv, ok := bVal.(map[string]interface{})
		if !ok {
			*diffs = append(*diffs, structDiff{path, render(aVal), render(bVal)})
			return
		}
		for _, key := range unionKeys(av, bv) {
			jv, jok := av[key]
			sv, sok := bv[key]
			switch {
			case jok && !sok:
				*diffs = append(*diffs, structDiff{childPath(path, key), render(jv), "<absent>"})
			case !jok && sok:
				*diffs = append(*diffs, structDiff{childPath(path, key), "<absent>", render(sv)})
			default:
				walkDiff(childPath(path, key), jv, sv, diffs)
			}
		}
	case []interface{}:
		bv, ok := bVal.([]interface{})
		if !ok {
			*diffs = append(*diffs, structDiff{path, render(aVal), render(bVal)})
			return
		}
		if len(av) != len(bv) {
			*diffs = append(*diffs, structDiff{
				path,
				fmt.Sprintf("%d item(s): %s", len(av), render(aVal)),
				fmt.Sprintf("%d item(s): %s", len(bv), render(bVal)),
			})
			return
		}
		for i := range av {
			walkDiff(fmt.Sprintf("%s[%d]", path, i), av[i], bv[i], diffs)
		}
	default:
		if !reflect.DeepEqual(aVal, bVal) {
			*diffs = append(*diffs, structDiff{path, render(aVal), render(bVal)})
		}
	}
}

func unionKeys(a, b map[string]interface{}) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// childPath renders map descent as [key] for name-keyed collections and .key
// otherwise, so paths read like spec.containers[agent].env[MODEL_NAME].value.
func childPath(parent, key string) string {
	for _, collection := range nameKeyedCollections {
		if strings.HasSuffix(parent, collection) {
			return fmt.Sprintf("%s[%s]", parent, key)
		}
	}
	return parent + "." + key
}

func render(v interface{}) string {
	s := fmt.Sprintf("%v", v)
	const max = 160
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// ── reflection fill ───────────────────────────────────────────────────────────

var quantityType = reflect.TypeOf(resource.Quantity{})

// fillStruct recursively sets every exported field of v to a non-zero value, so a
// field a converter or propagation path ignores shows up as a difference.
//
// Depth is capped: the core API nests deeply (PodSpec → Container → Probe → …) and
// callers inspect top-level output keys.
func fillStruct(t *testing.T, v reflect.Value, depth int) {
	t.Helper()
	if depth > 4 || !v.CanSet() {
		return
	}

	// resource.Quantity has unexported fields and cannot be built by reflection.
	if v.Type() == quantityType {
		v.Set(reflect.ValueOf(resource.MustParse("1")))
		return
	}

	switch v.Kind() {
	case reflect.String:
		// Enum-typed strings (corev1.PullPolicy, …) accept any value here; the
		// guards check only that a key was emitted.
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1)
	case reflect.Ptr:
		v.Set(reflect.New(v.Type().Elem()))
		fillStruct(t, v.Elem(), depth+1)
	case reflect.Slice:
		elem := reflect.New(v.Type().Elem()).Elem()
		fillStruct(t, elem, depth+1)
		v.Set(reflect.Append(reflect.MakeSlice(v.Type(), 0, 1), elem))
	case reflect.Map:
		key := reflect.New(v.Type().Key()).Elem()
		fillStruct(t, key, depth+1)
		val := reflect.New(v.Type().Elem()).Elem()
		fillStruct(t, val, depth+1)
		m := reflect.MakeMap(v.Type())
		m.SetMapIndex(key, val)
		v.Set(m)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).PkgPath != "" {
				continue // unexported
			}
			fillStruct(t, v.Field(i), depth+1)
		}
	}
}
