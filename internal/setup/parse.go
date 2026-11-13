package setup

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

const maxSuggestDistance = 2

func Parse(file string, raw []byte) (Spec, []error) {
	return parse(file, raw, false)
}

func ParseStored(file string, raw []byte) (Spec, []error) {
	return parse(file, raw, true)
}

func parse(file string, raw []byte, stored bool) (Spec, []error) {
	var s Spec
	d := &nodeDecoder{file: file, flatten: "spec", retired: []string{"start"}, stored: stored, pos: map[string]Pos{}}
	d.document(raw, reflect.ValueOf(&s).Elem())
	s.pos = d.pos
	if len(d.errs) > 0 && len(d.pos) == 0 {
		return s, d.errs
	}
	for _, p := range s.check() {
		if !d.failedUnder(p.path) {
			d.errs = append(d.errs, p.err)
		}
	}
	return s, d.errs
}

type nodeDecoder struct {
	file    string
	flatten string
	retired []string
	stored  bool
	pos     map[string]Pos
	failed  []string
	errs    []error
}

func (d *nodeDecoder) document(raw []byte, v reflect.Value) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var root yaml.Node
	if err := dec.Decode(&root); err != nil {
		if errors.Is(err, io.EOF) {
			d.errs = append(d.errs, fmt.Errorf("%s: the file is empty. Write the settings in it.", d.file))
			return
		}
		d.errs = append(d.errs, fmt.Errorf("%s: read yaml: %w", d.file, err))
		return
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		d.fail(&extra, "", "the file holds more than one document. Keep one setup per file")
		return
	} else if !errors.Is(err, io.EOF) {
		d.errs = append(d.errs, fmt.Errorf("%s: read yaml: %w", d.file, err))
		return
	}
	n := &root
	if n.Kind == yaml.DocumentNode && len(n.Content) == 1 {
		n = n.Content[0]
	}
	d.pos[""] = d.at(n)
	d.value(n, "", v)
}

func (d *nodeDecoder) at(n *yaml.Node) Pos {
	return Pos{File: d.file, Line: n.Line, Col: n.Column}
}

func (d *nodeDecoder) fail(n *yaml.Node, path, format string, args ...any) {
	msg := fmt.Sprintf(format, args...) + "."
	if path != "" {
		msg = path + " " + msg
	}
	d.failed = append(d.failed, path)
	d.errs = append(d.errs, fmt.Errorf("%s:%d:%d: %s", d.file, n.Line, n.Column, msg))
}

func (d *nodeDecoder) failedUnder(path string) bool {
	for p := path; ; p = specParent(p) {
		if slices.Contains(d.failed, p) {
			return true
		}
		if p == "" {
			return false
		}
	}
}

func (d *nodeDecoder) record(path string, n *yaml.Node) {
	if _, ok := d.pos[path]; !ok {
		d.pos[path] = d.at(n)
	}
}

func (d *nodeDecoder) value(n *yaml.Node, path string, v reflect.Value) {
	if n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	switch v.Kind() {
	case reflect.Struct:
		d.object(n, path, v)
	case reflect.Pointer:
		if n.Tag == "!!null" {
			return
		}
		p := reflect.New(v.Type().Elem())
		d.value(n, path, p.Elem())
		v.Set(p)
	case reflect.Slice:
		d.list(n, path, v)
	case reflect.Map:
		d.dict(n, path, v)
	case reflect.String:
		if n.Kind != yaml.ScalarNode {
			d.fail(n, path, "must be text, not a list or mapping. Write a single value")
			return
		}
		v.SetString(n.Value)
	case reflect.Int, reflect.Int64:
		var i int64
		if n.Kind != yaml.ScalarNode || n.Tag != "!!int" || n.Decode(&i) != nil || v.OverflowInt(i) {
			if strings.HasSuffix(path, "_minor") {
				d.fail(n, path, "%s is not a whole number. Give the amount in kobo, such as 150 for 1.50 naira", describeNode(n))
				return
			}
			d.fail(n, path, "%s is not a whole number. Write digits only, such as 4", describeNode(n))
			return
		}
		v.SetInt(i)
	case reflect.Float64:
		var f float64
		if n.Kind != yaml.ScalarNode || (n.Tag != "!!int" && n.Tag != "!!float") || n.Decode(&f) != nil {
			d.fail(n, path, "%s is not a number. Write a number such as 1", describeNode(n))
			return
		}
		v.SetFloat(f)
	case reflect.Bool:
		var b bool
		if n.Kind != yaml.ScalarNode || n.Tag != "!!bool" || n.Decode(&b) != nil {
			d.fail(n, path, "%s is not true or false. Write true or false", describeNode(n))
			return
		}
		v.SetBool(b)
	default:
		d.fail(n, path, "has a type this file cannot hold. Remove it")
	}
}

func describeNode(n *yaml.Node) string {
	if n.Kind == yaml.ScalarNode {
		return n.Value
	}
	return "this value"
}

func (d *nodeDecoder) object(n *yaml.Node, path string, v reflect.Value) {
	if n.Tag == "!!null" {
		return
	}
	if n.Kind != yaml.MappingNode {
		d.fail(n, path, "must be a mapping of fields. Write it as key: value lines")
		return
	}
	fields, names := yamlFields(v.Type())
	base := path
	if path == d.flatten {
		base = ""
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, val := n.Content[i], n.Content[i+1]
		idx, ok := fields[k.Value]
		if !ok && path == d.flatten && slices.Contains(d.retired, k.Value) {
			if !d.stored {
				d.errs = append(d.errs, fmt.Errorf("%s:%d:%d: %s is no longer used. Remove it.", d.file, k.Line, k.Column, k.Value))
			}
			continue
		}
		if !ok {
			d.unknown(k, base, names)
			continue
		}
		child := joinSpecPath(base, k.Value)
		if path == "" && k.Value == d.flatten {
			child = d.flatten
		}
		if seen[k.Value] {
			d.fail(k, child, "appears twice. Keep one")
			continue
		}
		seen[k.Value] = true
		d.record(child, k)
		d.value(val, child, v.Field(idx))
	}
}

func (d *nodeDecoder) unknown(k *yaml.Node, base string, names []string) {
	if s := suggestName(k.Value, names); s != "" {
		d.failed = append(d.failed, joinSpecPath(base, s))
		d.errs = append(d.errs, fmt.Errorf("%s:%d:%d: unknown field %q (did you mean %q?)", d.file, k.Line, k.Column, k.Value, s))
		return
	}
	d.errs = append(d.errs, fmt.Errorf("%s:%d:%d: unknown field %q. Remove it.", d.file, k.Line, k.Column, k.Value))
}

func (d *nodeDecoder) list(n *yaml.Node, path string, v reflect.Value) {
	if n.Tag == "!!null" {
		return
	}
	if n.Kind != yaml.SequenceNode {
		d.fail(n, path, "must be a list. Write it as [a, b] or as lines starting with -")
		return
	}
	if len(n.Content) == 0 {
		return
	}
	elem := v.Type().Elem()
	keys := make([]string, len(n.Content))
	for i, item := range n.Content {
		if elem.Kind() == reflect.Struct {
			keys[i] = mappingValue(item, "id")
		} else if item.Kind == yaml.ScalarNode {
			keys[i] = item.Value
		}
	}
	paths := specItemPaths(path, keys)
	out := reflect.MakeSlice(v.Type(), len(n.Content), len(n.Content))
	for i, item := range n.Content {
		d.record(paths[i], item)
		d.value(item, paths[i], out.Index(i))
	}
	v.Set(out)
}

func (d *nodeDecoder) dict(n *yaml.Node, path string, v reflect.Value) {
	if n.Tag == "!!null" {
		return
	}
	if n.Kind != yaml.MappingNode {
		d.fail(n, path, "must be a mapping. Write it as key: value lines")
		return
	}
	if len(n.Content) == 0 {
		return
	}
	out := reflect.MakeMapWithSize(v.Type(), len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, val := n.Content[i], n.Content[i+1]
		child := path + "[" + k.Value + "]"
		if k.Kind != yaml.ScalarNode {
			d.fail(k, path, "has a key that is not text. Use plain names as keys")
			continue
		}
		key := reflect.ValueOf(k.Value)
		if out.MapIndex(key).IsValid() {
			d.fail(k, child, "appears twice. Keep one")
			continue
		}
		d.record(child, k)
		e := reflect.New(v.Type().Elem()).Elem()
		d.value(val, child, e)
		out.SetMapIndex(key, e)
	}
	v.Set(out)
}

func mappingValue(n *yaml.Node, key string) string {
	if n.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key && n.Content[i+1].Kind == yaml.ScalarNode {
			return n.Content[i+1].Value
		}
	}
	return ""
}

func yamlFields(t reflect.Type) (map[string]int, []string) {
	fields := map[string]int{}
	var names []string
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "" || name == "-" {
			continue
		}
		fields[name] = i
		names = append(names, name)
	}
	return fields, names
}

func suggestName(got string, names []string) string {
	best, bestDist := "", maxSuggestDistance+1
	for _, n := range names {
		if d := editDistance(got, n); d < bestDist {
			best, bestDist = n, d
		}
	}
	return best
}

func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

func joinSpecPath(base, key string) string {
	if base == "" {
		return key
	}
	return base + "." + key
}

func specItemPaths(path string, keys []string) []string {
	out := make([]string, len(keys))
	seen := map[string]bool{}
	for i, k := range keys {
		if k != "" && !seen[k] && !strings.ContainsAny(k, "[]") && strings.Trim(k, "0123456789") != "" {
			out[i] = fmt.Sprintf("%s[%s]", path, k)
			seen[k] = true
			continue
		}
		out[i] = fmt.Sprintf("%s[%d]", path, i)
	}
	return out
}

func specParent(path string) string {
	if strings.HasSuffix(path, "]") {
		if i := strings.LastIndex(path, "["); i >= 0 {
			return path[:i]
		}
	}
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[:i]
	}
	switch path {
	case "", "apiVersion", "kind", "metadata", "spec":
		return ""
	}
	return "spec"
}
