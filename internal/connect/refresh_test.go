package connect

import (
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func TestNIBSSPortDefaultAndConflicts(t *testing.T) {
	ln := setup.LocalConnect{PartnerPort: 7443, WidgetPort: 7444, ControlPort: 7480, HealthPort: 7490}
	bankAt := func(port int) setup.Status {
		return setup.Status{Resources: []setup.Resource{{ID: "bank-a", Allocated: setup.Allocation{Port: port, APIPort: port + 1}}}}
	}
	rows := []struct {
		name string
		ln   setup.LocalConnect
		st   setup.Status
		port int
		err  string
	}{
		{"default", ln, setup.Status{}, defaultNIBSSPort, ""},
		{"set", setup.LocalConnect{PartnerPort: 7443, NIBSSPort: 9000}, setup.Status{}, 9000, ""},
		{"a connect port holds it", setup.LocalConnect{PartnerPort: 7443, HealthPort: defaultNIBSSPort}, setup.Status{}, 0, "NIBSS port 7481 is already used by connect.health_port; set connect.nibss_port in local.yaml to a free port"},
		{"a bank port holds it", ln, bankAt(defaultNIBSSPort), 0, "NIBSS port 7481 is already used by bank-a online banking; set connect.nibss_port in local.yaml to a free port"},
		{"a bank API port holds it", ln, bankAt(defaultNIBSSPort - 1), 0, "NIBSS port 7481 is already used by bank-a open banking API; set connect.nibss_port in local.yaml to a free port"},
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
