package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/setup"
)

const (
	fakeBankScript = "#!/bin/sh\nexec sleep 300\n"
	syncDeadline   = 10 * time.Second
	syncPoll       = 20 * time.Millisecond
)

func bankJSON(id, phase string, port int, db string) string {
	return fmt.Sprintf(`{"id":%q,"phase":%q,"port":%d,"api_port":%d,"database":%q}`, id, phase, port, port+1, db)
}

func listJSON(banks ...string) string {
	return `{"data":[` + strings.Join(banks, ",") + `],"warnings":[]}`
}

func newReconcileStack(t *testing.T) (*supervisor, func(string)) {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "fakebank")
	if err := os.WriteFile(exe, []byte(fakeBankScript), 0o700); err != nil {
		t.Fatal(err)
	}
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
	s := newSupervisor(exe, t.TempDir(), "http://"+lis.Addr().String(), &ctl{base: "http://" + lis.Addr().String()}, io.Discard)
	t.Cleanup(func() {
		srv.Close()
		for _, c := range s.banks {
			c.stop()
			<-c.done
		}
		for _, c := range s.stopping {
			<-c.done
		}
	})
	return s, func(v string) {
		mu.Lock()
		defer mu.Unlock()
		body = v
	}
}

func running(s *supervisor) []string {
	ids := []string{}
	for id, c := range s.banks {
		ids = append(ids, id+"="+c.print)
	}
	slices.Sort(ids)
	return ids
}

func TestReconcileBanks(t *testing.T) {
	s, serve := newReconcileStack(t)
	steps := []struct {
		name    string
		body    string
		wantErr string
		want    []string
	}{
		{"active and suspended start", listJSON(bankJSON("a", setup.PhaseActive, 100, "db_a"), bankJSON("b", setup.PhaseSuspended, 200, "db_b")), "", []string{"a=100/101/db_a", "b=200/201/db_b"}},
		{"null data changes nothing", `{"data":null}`, "read banks: the list is missing", []string{"a=100/101/db_a", "b=200/201/db_b"}},
		{"no data key changes nothing", `{}`, "read banks: the list is missing", []string{"a=100/101/db_a", "b=200/201/db_b"}},
		{"closed drops", listJSON(bankJSON("a", setup.PhaseClosed, 100, "db_a"), bankJSON("b", setup.PhaseActive, 200, "db_b")), "", []string{"b=200/201/db_b"}},
		{"changed fingerprint restarts", listJSON(bankJSON("b", setup.PhaseActive, 300, "db_b2")), "", []string{"b=300/301/db_b2"}},
		{"provisioning does not start", listJSON(bankJSON("b", setup.PhaseActive, 300, "db_b2"), bankJSON("c", setup.PhaseProvisioning, 400, "db_c")), "", []string{"b=300/301/db_b2"}},
		{"unlisted drops", listJSON(bankJSON("c", setup.PhaseProvisioning, 400, "db_c")), "", []string{}},
		{"restart after drop", listJSON(bankJSON("a", setup.PhaseActive, 100, "db_a")), "", []string{"a=100/101/db_a"}},
		{"empty list drops all", listJSON(), "", []string{}},
	}
	for _, st := range steps {
		serve(st.body)
		banks, err := s.readBanks(context.Background())
		if st.wantErr != "" {
			if err == nil || err.Error() != st.wantErr {
				t.Fatalf("%s: err %v, want %q", st.name, err, st.wantErr)
			}
			if got := running(s); !slices.Equal(got, st.want) {
				t.Fatalf("%s: running %v, want %v", st.name, got, st.want)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		deadline := time.Now().Add(syncDeadline)
		for {
			s.sync(banks)
			got := running(s)
			if slices.Equal(got, st.want) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: running %v, want %v", st.name, got, st.want)
			}
			<-time.After(syncPoll)
		}
	}
}
