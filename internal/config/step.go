package config

import "fmt"

const (
	StepAllocate          = "allocate"
	StepDirectory         = "directory"
	StepDatabase          = "database"
	StepLedgerSchema      = "ledger_schema"
	StepOpenBankingSchema = "openbanking_schema"
	StepCerts             = "certs"
	StepCBN               = "cbn"
	StepSite              = "site"
	StepAPI               = "api"
)

type BankStep struct {
	ID   string
	Name string
}

func BankSteps() []BankStep {
	return []BankStep{
		{StepAllocate, "Allocating codes"},
		{StepDirectory, "Registering with NIBSS"},
		{StepDatabase, "Creating the bank database"},
		{StepLedgerSchema, "Migrating bank ledger"},
		{StepOpenBankingSchema, "Migrating open banking"},
		{StepCerts, "Issuing certificates"},
		{StepCBN, "Opening the CBN settlement account"},
		{StepSite, "Starting online banking"},
		{StepAPI, "Starting the open banking API"},
	}
}

func (b *Bank) RunningPhase() string {
	if b.Suspended() {
		return PhaseSuspended
	}
	return PhaseActive
}

func MigrationCount(n int) string {
	switch n {
	case 0:
		return "no new migrations"
	case 1:
		return "1 migration"
	}
	return fmt.Sprintf("%d migrations", n)
}
