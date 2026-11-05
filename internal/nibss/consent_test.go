package nibss

import (
	"context"
	"testing"
	"time"
)

func consent(bank, id string, src int64, at time.Time) ConsentEvent {
	return ConsentEvent{Bank: bank, ConsentID: id, ClientID: "client-1", Status: "Authorised", ChangedAt: at, SourceSeq: src}
}

func TestConsentLedgerPaging(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	events := []ConsentEvent{
		consent("bank-a", "c1", 1, t0),
		consent("bank-b", "c2", 1, t0),
		consent("bank-a", "c3", 2, t0),
		consent("bank-a", "c3", 2, t0),
		consent("bank-a", "c1", 1, t0),
		consent("bank-a", "c4", 0, t0),
		consent("bank-a", "c4", 0, t0),
		consent("bank-a", "c5", 3, t0),
	}
	for _, e := range events {
		if err := s.RecordConsent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	page, next, err := s.ConsentLedger(ctx, "bank-a", 2, "")
	if err != nil || len(page) != 2 || page[0].ConsentID != "c5" || page[1].ConsentID != "c4" || next != "5" {
		t.Fatalf("page 1: %+v, %q, %v", page, next, err)
	}
	page, next, err = s.ConsentLedger(ctx, "bank-a", 2, next)
	if err != nil || len(page) != 2 || page[0].ConsentID != "c4" || page[1].ConsentID != "c3" || next != "3" {
		t.Fatalf("page 2: %+v, %q, %v", page, next, err)
	}
	page, next, err = s.ConsentLedger(ctx, "bank-a", 2, next)
	if err != nil || len(page) != 1 || page[0].ConsentID != "c1" || next != "" {
		t.Fatalf("last page: %+v, %q, %v", page, next, err)
	}
	all, next, err := s.ConsentLedger(ctx, "", 10, "")
	if err != nil || len(all) != 6 || next != "" || all[5].Bank != "bank-a" || all[4].Bank != "bank-b" {
		t.Fatalf("all banks: %+v, %q, %v", all, next, err)
	}
	exact, next, err := s.ConsentLedger(ctx, "bank-b", 1, "")
	if err != nil || len(exact) != 1 || next != "" {
		t.Fatalf("exact page: %+v, %q, %v", exact, next, err)
	}
	if !all[0].ChangedAt.Equal(t0) || all[0].SourceSeq != 3 || all[0].ClientID != "client-1" || all[0].Status != "Authorised" {
		t.Errorf("stored event: %+v", all[0])
	}
}

func TestConsentLedgerRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	for _, c := range []struct {
		limit  int
		cursor string
		want   string
	}{
		{0, "", "limit must be from 1 to 1000, not 0"},
		{1001, "", "limit must be from 1 to 1000, not 1001"},
		{10, "abc", `cursor "abc" is not valid; use the next value from the last page`},
		{10, "0", `cursor "0" is not valid; use the next value from the last page`},
	} {
		_, _, err := s.ConsentLedger(ctx, "", c.limit, c.cursor)
		wantErr(t, err, c.want)
	}
	wantErr(t, s.RecordConsent(ctx, consent("", "c1", 1, t0)), "a consent event needs a bank and a consent id")
	wantErr(t, s.RecordConsent(ctx, consent("bank-a", "c1", 1, time.Time{})), "consent c1 event needs a changed time")
	wantErr(t, s.RecordConsent(ctx, consent("bank-a", "c1", -1, t0)), "consent c1 event: source seq -1 is negative")
	bad := consent("bank-a", "c1", 1, t0)
	bad.Status = "Maybe"
	if err := s.RecordConsent(ctx, bad); err == nil {
		t.Fatal("unknown consent status was stored")
	}
}
