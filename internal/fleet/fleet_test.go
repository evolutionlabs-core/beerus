package fleet

import (
	"context"
	"fmt"
	"io"
	"log/slog"
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

type reply func(*http.Request) (*http.Response, error)

func (f reply) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func newFleet(t *testing.T) (*Fleet, *recorder, func(string)) {
	t.Helper()
	body := ""
	rec := &recorder{dones: map[string]chan struct{}{}}
	f := New("http://control.invalid", "", rec, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.client.Transport = reply(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})
	t.Cleanup(f.Close)
	return f, rec, func(v string) { body = v }
}

func bankJSON(id, phase string, port int, db string) string {
	return fmt.Sprintf(`{"id":%q,"phase":%q,"port":%d,"api_port":%d,"database":%q}`, id, phase, port, port+1, db)
}

func listJSON(banks ...string) string {
	return `{"data":[` + strings.Join(banks, ",") + `],"warnings":[]}`
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
	a, b := "a=100/101/db_a", "b=200/201/db_b"
	steps := []struct {
		name, body, wantErr string
		want, started       []string
	}{
		{"active and suspended start", listJSON(bankJSON("a", config.PhaseActive, 100, "db_a"), bankJSON("b", config.PhaseSuspended, 200, "db_b")), "", []string{a, b}, []string{"a", "b"}},
		{"null data changes nothing", `{"data":null}`, "read banks: the list is missing", []string{a, b}, []string{"a", "b"}},
		{"closed drops", listJSON(bankJSON("a", config.PhaseClosed, 100, "db_a"), bankJSON("b", config.PhaseActive, 200, "db_b")), "", []string{b}, []string{"a", "b"}},
		{"changed fingerprint restarts", listJSON(bankJSON("b", config.PhaseActive, 300, "db_b2")), "", []string{"b=300/301/db_b2"}, []string{"a", "b", "b"}},
		{"provisioning does not start", listJSON(bankJSON("c", config.PhaseProvisioning, 400, "db_c")), "", []string{}, []string{"a", "b", "b"}},
		{"restart after drop", listJSON(bankJSON("a", config.PhaseActive, 100, "db_a")), "", []string{a}, []string{"a", "b", "b", "a"}},
	}
	for _, st := range steps {
		serve(st.body)
		banks, err := f.read(context.Background())
		if st.wantErr != "" || err != nil {
			if err == nil || err.Error() != st.wantErr {
				t.Fatalf("%s: err %v, want %q", st.name, err, st.wantErr)
			}
		} else {
			f.sync(banks)
			f.sync(banks)
		}
		if got := running(f); !slices.Equal(got, st.want) || !slices.Equal(rec.started, st.started) {
			t.Fatalf("%s: running %v started %v, want %v and %v", st.name, got, rec.started, st.want, st.started)
		}
	}
}

func TestFleetWaitsForAStoppingBank(t *testing.T) {
	f, rec, _ := newFleet(t)
	rec.slow = true
	active := []bank{{ID: "a", Phase: config.PhaseActive, Port: 100, APIPort: 101, Database: "db_a"}}
	f.sync(active)
	f.sync([]bank{{ID: "a", Phase: config.PhaseClosed}})
	f.sync(active)
	if got := running(f); len(got) != 0 || len(rec.started) != 1 {
		t.Fatalf("while the old process is still stopping: running %v, started %v", got, rec.started)
	}
	close(rec.dones["a"])
	f.sync(active)
	if got := running(f); !slices.Equal(got, []string{"a=100/101/db_a"}) || len(rec.started) != 2 {
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
		{"closed", bank{Phase: config.PhaseClosed}, false},
		{"provisioning before the CBN step", provisioning(config.PhaseProvisioning, false), false},
		{"repairing after the CBN step", provisioning(config.PhaseRepairing, true), true},
	} {
		if got := c.bank.startable(); got != c.want {
			t.Errorf("%s: startable %v, want %v", c.name, got, c.want)
		}
	}
}
