package nibss

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"
)

func namedDB(t *testing.T, prefix string) string {
	t.Helper()
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	name := "beerus_" + prefix + hex.EncodeToString(b)
	t.Cleanup(func() {
		c := testConn(context.Background(), t, "")
		_, _ = c.Exec(context.Background(), `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
		c.Close(context.Background())
	})
	return name
}

func TestSnapshotRollsBackOnlySessions(t *testing.T) {
	ctx := context.Background()
	testConn(ctx, t, "").Close(ctx)
	live, snap := namedDB(t, "nibss_live"), namedDB(t, "nibss_snap")
	s, err := Open(ctx, testBaseURL(), live)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	register(t, s, "bank-a", "Bank A", "999001", "901")
	register(t, s, "bank-b", "Bank B", "999002", "902")
	if _, err := s.Begin(ctx, transfer("s1", "999002", t0)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnrolBVN(ctx, "Adaeze Okafor", "08031234567", adaezeDOB); err != nil {
		t.Fatal(err)
	}
	if err := s.Snapshot(ctx, testBaseURL(), snap); err != nil {
		t.Fatal(err)
	}
	wantErr(t, s.Snapshot(ctx, testBaseURL(), snap), "NIBSS snapshot "+snap+" already holds data")

	if _, err := s.Begin(ctx, transfer("s2", "999002", t0)); err != nil {
		t.Fatal(err)
	}
	later, err := s.EnrolBVN(ctx, "Chidi Eze", "08039876543", adaezeDOB)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []string{StatusTerminating, StatusRemoved} {
		if err := s.SetStatus(ctx, "bank-b", st); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Restore(ctx, testBaseURL(), snap); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Session(ctx, "s1"); err != nil {
		t.Fatalf("the session from before the snapshot: %v", err)
	}
	if _, err := s.Session(ctx, "s2"); !errors.Is(err, ErrNoRecord) {
		t.Fatalf("the session from after the snapshot: %v", err)
	}
	if again, err := s.EnrolBVN(ctx, "Chidi Eze", "08039876543", adaezeDOB); err != nil || again != later {
		t.Fatalf("a BVN issued after the snapshot came back as %s, %v; want %s", again, err, later)
	}
	if in, err := s.Institution(ctx, "999002"); err != nil || in.Status != StatusRemoved {
		t.Fatalf("a removed bank after restore: %+v %v", in, err)
	}
	missing := namedDB(t, "nibss_none")
	wantErr(t, s.Restore(ctx, testBaseURL(), missing), "NIBSS snapshot "+missing+" does not exist")
}
