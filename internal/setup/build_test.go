package setup

import (
	"maps"
	"slices"
	"testing"
)

func TestNIBSSPortDefaultAndConflicts(t *testing.T) {
	ln := LocalConnect{PartnerPort: 7443, WidgetPort: 7444, ControlPort: 7480, HealthPort: 7490}
	bankAt := func(port int) Status {
		return Status{Resources: []Resource{{ID: "bank-a", Allocated: Allocation{Port: port, APIPort: port + 1}}}}
	}
	rows := []struct {
		name string
		ln   LocalConnect
		st   Status
		port int
		err  string
	}{
		{"default", ln, Status{}, DefaultNIBSSPort, ""},
		{"set", LocalConnect{PartnerPort: 7443, NIBSSPort: 9000}, Status{}, 9000, ""},
		{"a connect port holds it", LocalConnect{PartnerPort: 7443, HealthPort: DefaultNIBSSPort}, Status{}, 0, "NIBSS port 7481 is already used by connect.health_port; set connect.nibss_port in local.yaml to a free port"},
		{"a bank port holds it", ln, bankAt(DefaultNIBSSPort), 0, "NIBSS port 7481 is already used by bank-a online banking; set connect.nibss_port in local.yaml to a free port"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			port, err := nibssPort(r.ln, r.st)
			if r.err == "" && (err != nil || port != r.port) {
				t.Fatalf("port %d, error %v; want %d", port, err, r.port)
			}
			if r.err != "" && (err == nil || err.Error() != r.err) {
				t.Fatalf("error %v, want %q", err, r.err)
			}
		})
	}
}

func TestEveryBankTypeHasAHistory(t *testing.T) {
	if !slices.Equal(slices.Sorted(maps.Keys(typeHistoryDays)), BankTypes) {
		t.Fatalf("history days for %v, bank types %v", slices.Sorted(maps.Keys(typeHistoryDays)), BankTypes)
	}
}
