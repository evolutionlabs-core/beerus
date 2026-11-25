package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const stopDeadline = 10 * time.Second

func TestBankRunnerStopsItsProcess(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "fakebank")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nexec sleep 300\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	s := newSupervisor(exe, t.TempDir(), "http://127.0.0.1:1", "", &ctl{base: "http://127.0.0.1:1"}, io.Discard)
	stop, done := bankRunner{s}.Start("bank-a")
	t.Cleanup(func() {
		stop()
		<-done
	})
	stop()
	stop()
	select {
	case <-done:
	case <-time.After(stopDeadline):
		t.Fatalf("the process was still running %s after stop", stopDeadline)
	}
}
