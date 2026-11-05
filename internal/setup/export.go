package setup

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"reflect"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

//go:embed default.yaml
var defaultSetup []byte

const (
	exportIndent     = 2
	defaultStartHour = 8
	defaultFile      = "default.yaml"
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

func DefaultSpec(name string, today clock.Date) Spec {
	var s Spec
	d := &nodeDecoder{file: defaultFile, flatten: "spec", pos: map[string]Pos{}}
	d.document(defaultSetup, reflect.ValueOf(&s).Elem())
	if len(d.errs) > 0 {
		panic(fmt.Errorf("decode embedded %s: %w", defaultFile, errors.Join(d.errs...)))
	}
	s.Metadata.Name = name
	s.Spec.Start = time.Date(today.Y, today.M, today.D, defaultStartHour, 0, 0, 0, clock.Lagos).Format(time.RFC3339)
	return s
}
