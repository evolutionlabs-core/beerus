package fleet

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/config"
)

type recorder struct {
	mu      sync.Mutex
	slow    bool
	started []string
	dones   map[string]chan struct{}
}

func (r *recorder) Start(id string) (func(), <-chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started = append(r.started, id)
	done := make(chan struct{})
	if r.dones == nil {
		r.dones = map[string]chan struct{}{}
	}
	r.dones[id] = done
	var once sync.Once
	return func() {
		once.Do(func() {
			if !r.slow {
				close(done)
			}
		})
	}, done
}

func (r *recorder) finish(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	close(r.dones[id])
}

func bankJSON(id, phase string, port int, db string) string {
	return fmt.Sprintf(`{"id":%q,"phase":%q,"port":%d,"api_port":%d,"database":%q}`, id, phase, port, port+1, db)
}

func listJSON(banks ...string) string {
	return `{"data":[` + strings.Join(banks, ",") + `],"warnings":[]}`
}

func newFleet(t *testing.T) (*Fleet, *recorder, func(string)) {
	t.Helper()
	var mu sync.Mutex
	body := ""
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = io.WriteString(w, body)
	})}
	go srv.Serve(lis)
	rec := &recorder{}
	f := New("http://"+lis.Addr().String(), "", rec, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() {
		srv.Close()
		f.Close()
	})
	return f, rec, func(v string) {
		mu.Lock()
		defer mu.Unlock()
		body = v
	}
}

func running(f *Fleet) []string {
	ids := []string{}
	for id, m := range f.banks {
		ids = append(ids, id+"="+m.print)
	}
	slices.Sort(ids)
	return ids
}

func TestFleetSync(t *testing.T) {
	f, rec, serve := newFleet(t)
	steps := []struct {
		name    string
		body    string
		wantErr string
		want    []string
		started []string
	}{
		{"active and suspended start", listJSON(bankJSON("a", config.PhaseActive, 100, "db_a"), bankJSON("b", config.PhaseSuspended, 200, "db_b")), "", []string{"a=100/101/db_a", "b=200/201/db_b"}, []string{"a", "b"}},
		{"null data changes nothing", `{"data":null}`, "read banks: the list is missing", []string{"a=100/101/db_a", "b=200/201/db_b"}, []string{"a", "b"}},
		{"no data key changes nothing", `{}`, "read banks: the list is missing", []string{"a=100/101/db_a", "b=200/201/db_b"}, []string{"a", "b"}},
		{"closed drops", listJSON(bankJSON("a", config.PhaseClosed, 100, "db_a"), bankJSON("b", config.PhaseActive, 200, "db_b")), "", []string{"b=200/201/db_b"}, []string{"a", "b"}},
		{"changed fingerprint restarts", listJSON(bankJSON("b", config.PhaseActive, 300, "db_b2")), "", []string{"b=300/301/db_b2"}, []string{"a", "b", "b"}},
		{"provisioning does not start", listJSON(bankJSON("b", config.PhaseActive, 300, "db_b2"), bankJSON("c", config.PhaseProvisioning, 400, "db_c")), "", []string{"b=300/301/db_b2"}, []string{"a", "b", "b"}},
		{"unlisted drops", listJSON(bankJSON("c", config.PhaseProvisioning, 400, "db_c")), "", []string{}, []string{"a", "b", "b"}},
		{"restart after drop", listJSON(bankJSON("a", config.PhaseActive, 100, "db_a")), "", []string{"a=100/101/db_a"}, []string{"a", "b", "b", "a"}},
		{"empty list drops all", listJSON(), "", []string{}, []string{"a", "b", "b", "a"}},
	}
	for _, st := range steps {
		serve(st.body)
		banks, err := f.read(context.Background())
		if st.wantErr != "" {
			if err == nil || err.Error() != st.wantErr {
				t.Fatalf("%s: err %v, want %q", st.name, err, st.wantErr)
			}
		} else if err != nil {
			t.Fatalf("%s: %v", st.name, err)
		} else {
			f.sync(banks)
			f.sync(banks)
		}
		if got := running(f); !slices.Equal(got, st.want) {
			t.Fatalf("%s: running %v, want %v", st.name, got, st.want)
		}
		if !slices.Equal(rec.started, st.started) {
			t.Fatalf("%s: started %v, want %v", st.name, rec.started, st.started)
		}
	}
}

func TestFleetWaitsForAStoppingBank(t *testing.T) {
	f, rec, _ := newFleet(t)
	rec.slow = true
	active := []bank{{ID: "a", Phase: config.PhaseActive, Port: 100, APIPort: 101, Database: "db_a"}}
	closed := []bank{{ID: "a", Phase: config.PhaseClosed, Port: 100, APIPort: 101, Database: "db_a"}}
	f.sync(active)
	f.sync(closed)
	f.sync(active)
	if got := running(f); len(got) != 0 || !slices.Equal(rec.started, []string{"a"}) {
		t.Fatalf("while the old process is still stopping: running %v, started %v", got, rec.started)
	}
	rec.finish("a")
	f.sync(active)
	if got := running(f); !slices.Equal(got, []string{"a=100/101/db_a"}) || !slices.Equal(rec.started, []string{"a", "a"}) {
		t.Fatalf("after the old process stopped: running %v, started %v", got, rec.started)
	}
	rec.slow = false
}

func TestStartable(t *testing.T) {
	provisioning := func(phase string, done bool) bank {
		b := bank{Phase: phase}
		b.Steps = append(b.Steps, struct {
			ID   string `json:"id"`
			Done bool   `json:"done"`
		}{config.StepCBN, done})
		return b
	}
	for _, c := range []struct {
		name string
		bank bank
		want bool
	}{
		{"active", bank{Phase: config.PhaseActive}, true},
		{"suspended", bank{Phase: config.PhaseSuspended}, true},
		{"closing", bank{Phase: config.PhaseClosing}, true},
		{"terminating", bank{Phase: config.PhaseTerminating}, true},
		{"closed", bank{Phase: config.PhaseClosed}, false},
		{"provisioning before the CBN step", provisioning(config.PhaseProvisioning, false), false},
		{"provisioning after the CBN step", provisioning(config.PhaseProvisioning, true), true},
		{"repairing after the CBN step", provisioning(config.PhaseRepairing, true), true},
		{"provisioning without the step", bank{Phase: config.PhaseProvisioning}, false},
	} {
		if got := c.bank.startable(); got != c.want {
			t.Errorf("%s: startable %v, want %v", c.name, got, c.want)
		}
	}
}
