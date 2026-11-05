package setup

import (
	"bytes"
	"fmt"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

const (
	exportIndent       = 2
	defaultStartHour   = 8
	defaultTSQAfter    = "2m"
	defaultTSQAttempts = 3
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
	start := time.Date(today.Y, today.M, today.D, defaultStartHour, 0, 0, 0, clock.Lagos)
	return Spec{
		APIVersion: APIVersion,
		Kind:       KindSetup,
		Metadata:   Metadata{Name: name},
		Spec: Body{
			Start: start.Format(time.RFC3339),
			NIP:   NIP{Sessions: config.DefaultNIPSessions, TSQAfter: defaultTSQAfter, TSQAttempts: defaultTSQAttempts},
			Connect: ConnectLegal{
				LegalName:  "Beerus Connect Ltd",
				BrandName:  "Beerus",
				RegistryID: "OB-0001",
				CACNumber:  "RC-000001",
			},
			Banks: []Bank{
				{ID: "bank-a", Name: "Bank A", Type: "commercial", NIPCode: "999001", NUBANCode: "901"},
				{ID: "bank-b", Name: "Bank B", Type: "digital", NIPCode: "999002", NUBANCode: "902"},
			},
		},
	}
}
