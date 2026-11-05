package world

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

const (
	GeneratorVersion = 1
	TemplateRetail   = "retail"
	MaxHistoryDays   = 730
)

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
	if template != TemplateRetail {
		return Template{}, fmt.Errorf("unknown template %q; known templates: %s", template, strings.Join(knownTemplates, ", "))
	}
	if cal == nil {
		return Template{}, errors.New("generate seed: no holiday calendar")
	}
	seen := map[string]bool{}
	for i, b := range banks {
		if b.ID == "" {
			return Template{}, fmt.Errorf("bank %d in the list has no id", i+1)
		}
		if b.Style != "traditional" && b.Style != "digital" {
			return Template{}, fmt.Errorf("bank %s has style %q; it must be traditional or digital", b.ID, b.Style)
		}
		if seen[b.ID] {
			return Template{}, fmt.Errorf("bank %s is listed twice", b.ID)
		}
		seen[b.ID] = true
	}
	roster := retailRoster(value)
	out := Template{Customers: []SeedCustomer{}, Accounts: []SeedAccount{}, Postings: []SeedPosting{}}
	for _, b := range banks {
		t := generateRetail(roster, value, b, banks, historyDays, now, cal)
		out.Customers = append(out.Customers, t.Customers...)
		out.Accounts = append(out.Accounts, t.Accounts...)
		out.Postings = append(out.Postings, t.Postings...)
	}
	return out, nil
}
