package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	fakeBankScript = "#!/bin/sh\nexec sleep 300\n"
	stopDeadline   = 10 * time.Second
	stopPoll       = 5 * time.Millisecond
)

func TestBankRunnerStopsItsProcess(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "fakebank")
	if err := os.WriteFile(exe, []byte(fakeBankScript), 0o700); err != nil {
		t.Fatal(err)
	}
	s := newSupervisor(exe, t.TempDir(), "http://127.0.0.1:1", "", &ctl{base: "http://127.0.0.1:1"}, io.Discard)
	stop, done := bankRunner{s}.Start("bank-a")
	t.Cleanup(func() {
		stop()
		<-done
	})
	select {
	case <-done:
		t.Fatal("the process ended before it was stopped")
	default:
	}
	stop()
	stop()
	deadline := time.NewTimer(stopDeadline)
	defer deadline.Stop()
	tick := time.NewTicker(stopPoll)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return
		case <-deadline.C:
			t.Fatalf("the process was still running %s after stop", stopDeadline)
		case <-tick.C:
		}
	}
}
