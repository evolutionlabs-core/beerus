package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/nip"
)

const faultIDHexChars = 20

var ErrInvalid = errors.New("invalid bank")

func Invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

func FaultID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "flt_" + hex.EncodeToString(sum[:])[:faultIDHexChars]
}

const (
	FaultDown    = "down"
	FaultTimeout = "timeout"
)

type Fault struct {
	ID      string     `json:"id"`
	Kind    string     `json:"kind"`
	Bank    string     `json:"bank"`
	Outcome string     `json:"outcome,omitempty"`
	Left    int        `json:"left,omitempty"`
	Until   *time.Time `json:"until,omitempty"`
	At      time.Time  `json:"created_at"`
}

func (f *Fault) Live(now time.Time) bool {
	switch f.Kind {
	case FaultDown:
		return f.Until == nil || now.Before(*f.Until)
	case FaultTimeout:
		return f.Left > 0
	}
	return false
}

type FaultRequest struct {
	Kind    string `json:"kind"`
	Bank    string `json:"bank"`
	Outcome string `json:"outcome"`
	Count   *int   `json:"count"`
	For     string `json:"for"`
}

const MaxFaultFor = 24 * time.Hour

func NewFault(req FaultRequest, now time.Time) (Fault, error) {
	f := Fault{Kind: req.Kind, At: now}
	switch req.Kind {
	case FaultDown:
		if req.Outcome != "" || req.Count != nil {
			return Fault{}, Invalid("a down fault takes only bank and for")
		}
		if req.For != "" {
			d, err := time.ParseDuration(req.For)
			if err != nil || d <= 0 {
				return Fault{}, Invalid("for %q is not a duration above zero, such as 1h", req.For)
			}
			if d > MaxFaultFor {
				return Fault{}, Invalid("for %q is above the limit of %dh", req.For, int(MaxFaultFor.Hours()))
			}
			until := now.Add(d)
			f.Until = &until
		}
	case FaultTimeout:
		if !slices.Contains([]string{nip.OutcomeCredited, nip.OutcomeFailed, nip.OutcomeUnknown}, req.Outcome) {
			return Fault{}, Invalid("outcome %q must be credited, failed or unknown", req.Outcome)
		}
		if req.For != "" {
			return Fault{}, Invalid("a timeout fault takes a count, not for")
		}
		left := 1
		if req.Count != nil {
			if *req.Count < 1 {
				return Fault{}, Invalid("count %d must be 1 or more", *req.Count)
			}
			left = *req.Count
		}
		f.Outcome, f.Left = req.Outcome, left
	default:
		return Fault{}, Invalid("kind %q must be down or timeout", req.Kind)
	}
	return f, nil
}
