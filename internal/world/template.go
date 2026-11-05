package world

import (
	"errors"
	"fmt"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

const (
	GeneratorVersion = 1
	TemplateRetail   = "retail"
	MaxHistoryDays   = 730
)

var errNotBuilt = errors.New("not built")

type SeedCustomer struct {
	Key      string     `json:"key"`
	Bank     string     `json:"bank"`
	Name     string     `json:"name"`
	Phone    string     `json:"phone"`
	DOB      clock.Date `json:"dob"`
	Username string     `json:"username,omitempty"`
	Password string     `json:"-"`
}

type SeedAccount struct {
	Key      string    `json:"key"`
	Bank     string    `json:"bank"`
	Customer string    `json:"customer"`
	Product  string    `json:"product"`
	Name     string    `json:"name"`
	Opening  int64     `json:"opening_minor"`
	OpenedAt time.Time `json:"opened_at"`
}

type SeedPosting struct {
	Key              string     `json:"key"`
	Bank             string     `json:"bank"`
	Account          string     `json:"account"`
	PostedAt         time.Time  `json:"posted_at"`
	ValueDate        clock.Date `json:"value_date"`
	Amount           int64      `json:"amount_minor"`
	Narration        string     `json:"narration"`
	Channel          string     `json:"channel"`
	Category         string     `json:"category,omitempty"`
	Counterparty     string     `json:"counterparty,omitempty"`
	CounterpartyBank string     `json:"counterparty_bank,omitempty"`
}

type Template struct {
	Customers []SeedCustomer `json:"customers"`
	Accounts  []SeedAccount  `json:"accounts"`
	Postings  []SeedPosting  `json:"postings"`
}

func Generate(template string, value int64, banks []Institution, historyDays int, now time.Time, cal *clock.Calendar) (Template, error) {
	if historyDays < 0 || historyDays > MaxHistoryDays {
		return Template{}, fmt.Errorf("history is %d days; it must be 0 to %d", historyDays, MaxHistoryDays)
	}
	return Template{}, errNotBuilt
}
