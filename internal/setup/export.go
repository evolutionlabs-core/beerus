package setup

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"reflect"

	"go.yaml.in/yaml/v3"
)

//go:embed default.yaml
var defaultSetup []byte

const (
	exportIndent = 2
	defaultFile  = "default.yaml"
)

func Export(s Spec) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(exportIndent)
	if err := enc.Encode(s); err != nil {
		return nil, fmt.Errorf("encode setup %s: %w", s.Metadata.Name, err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode setup %s: %w", s.Metadata.Name, err)
	}
	return buf.Bytes(), nil
}

func DefaultSpec(name string) Spec {
	var s Spec
	d := &nodeDecoder{file: defaultFile, flatten: "spec", pos: map[string]Pos{}}
	d.document(defaultSetup, reflect.ValueOf(&s).Elem())
	if len(d.errs) > 0 {
		panic(fmt.Errorf("decode embedded %s: %w", defaultFile, errors.Join(d.errs...)))
	}
	s.Metadata.Name = name
	return s
}
