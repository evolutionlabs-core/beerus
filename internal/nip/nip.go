package nip

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

var (
	ErrUnavailable    = errors.New("91 institution unavailable")
	ErrInvalidAccount = errors.New("07 invalid account")
	ErrNoRecord       = errors.New("25 no record")
)

const (
	OK             = "00"
	InvalidAccount = "07"
	NoRecord       = "25"
	NoFunds        = "51"
	Restricted     = "57"
	OverCap        = "61"
	Unavailable    = "91"
	SystemError    = "96"

	BVNPrefix = "229"

	Success  = "success"
	Failed   = "failed"
	Reversed = "reversed"
	Pending  = "pending"
)

const (
	defaultCapCommercial   int64 = 500_000_000_000
	defaultCapMicrofinance int64 = 50_000_000_000
)

func DefaultCap(bankType string) int64 {
	if bankType == "microfinance" {
		return defaultCapMicrofinance
	}
	return defaultCapCommercial
}

type Participant struct {
	Code    string `json:"nip_code"`
	Name    string `json:"name"`
	BankID  string `json:"bank_id,omitempty"`
	Outside bool   `json:"outside"`
}

type Directory struct {
	byCode map[string]Participant
}

func NewDirectory(members, outside []Participant) *Directory {
	d := &Directory{byCode: map[string]Participant{}}
	for _, p := range members {
		d.byCode[p.Code] = p
	}
	for _, p := range outside {
		if _, taken := d.byCode[p.Code]; !taken {
			p.Outside = true
			d.byCode[p.Code] = p
		}
	}
	return d
}

func (d *Directory) Lookup(code string) (Participant, bool) {
	p, ok := d.byCode[code]
	return p, ok
}

func (d *Directory) List() []Participant {
	out := make([]Participant, 0, len(d.byCode))
	for _, p := range d.byCode {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

func digits(seed int64, label string, n int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%s", seed, label)))
	out := make([]byte, 0, n)
	for i := 0; len(out) < n; i++ {
		v := binary.BigEndian.Uint32(sum[(i*4)%28 : (i*4)%28+4])
		for k := 0; k < 9 && len(out) < n; k++ {
			out = append(out, byte('0'+v%10))
			v /= 10
		}
	}
	return string(out)
}

func BVN(seed int64, personaID string) string {
	return BVNPrefix + digits(seed, "bvn|"+personaID, 8)
}

func SessionID(seed int64, code string, at time.Time, seq int) string {
	return code + at.In(clock.Lagos).Format("060102150405") + digits(seed, fmt.Sprintf("sid|%s|%d", code, seq), 12)
}

func ValidSessionID(s string) bool {
	if len(s) != 30 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
