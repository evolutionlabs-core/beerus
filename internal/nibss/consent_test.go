package nibss

import (
	"context"
	"errors"
	"testing"
	"time"
)

func consent(bank, id string, src int64, at time.Time) ConsentEvent {
	return ConsentEvent{Bank: bank, ConsentID: id, ClientID: "client-1", PartnerID: "balance", Status: "Authorised", Via: "bank", ChangedAt: at, SourceSeq: src}
}

func TestRecordConsent(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	first, replayed, err := s.RecordConsent(ctx, consent("bank-a", "c1", 1, t0))
	if err != nil || replayed || first.Seq != 1 || first.PartnerID != "balance" || first.Via != "bank" || !first.ChangedAt.Equal(t0) {
		t.Fatalf("first record: %+v, %v, %v", first, replayed, err)
	}
	again, replayed, err := s.RecordConsent(ctx, consent("bank-a", "c1", 1, t0))
	if err != nil || !replayed || again != first {
		t.Fatalf("replay: %+v, %v, %v", again, replayed, err)
	}
	other, replayed, err := s.RecordConsent(ctx, consent("bank-b", "c1", 1, t0))
	if err != nil || replayed || other.Seq != 2 {
		t.Fatalf("same source seq at another bank: %+v, %v, %v", other, replayed, err)
	}
	late, replayed, err := s.RecordConsent(ctx, consent("bank-a", "c0", 2, t0))
	if err != nil || replayed || late.Seq != 3 {
		t.Fatalf("next source seq: %+v, %v, %v", late, replayed, err)
	}
	changed := consent("bank-a", "c1", 1, t0.Add(time.Hour))
	changed.Status = "Revoked"
	_, _, err = s.RecordConsent(ctx, changed)
	var reused *ConsentReusedError
	if !errors.As(err, &reused) {
		t.Fatalf("a changed repeat: %v", err)
	}
	wantErr(t, err, "source_seq 1 of bank bank-a was first recorded with other status and changed_at")
}

func TestConsentLedgerPaging(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	events := []ConsentEvent{
		consent("bank-a", "c1", 1, t0),
		consent("bank-b", "c2", 1, t0),
		consent("bank-a", "c3", 2, t0),
		consent("bank-a", "c4", 3, t0),
		consent("bank-a", "c5", 4, t0),
	}
	for _, e := range events {
		if _, _, err := s.RecordConsent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	page, next, err := s.ConsentLedger(ctx, "bank-a", 2, "")
	if err != nil || len(page) != 2 || page[0].ConsentID != "c5" || page[1].ConsentID != "c4" || next != "4" {
		t.Fatalf("page 1: %+v, %q, %v", page, next, err)
	}
	page, next, err = s.ConsentLedger(ctx, "bank-a", 2, next)
	if err != nil || len(page) != 2 || page[0].ConsentID != "c3" || page[1].ConsentID != "c1" || next != "" {
		t.Fatalf("page 2: %+v, %q, %v", page, next, err)
	}
	all, next, err := s.ConsentLedger(ctx, "", 10, "")
	if err != nil || len(all) != 5 || next != "" || all[3].Bank != "bank-b" {
		t.Fatalf("all banks: %+v, %q, %v", all, next, err)
	}
	exact, next, err := s.ConsentLedger(ctx, "bank-b", 1, "")
	if err != nil || len(exact) != 1 || next != "" {
		t.Fatalf("exact page: %+v, %q, %v", exact, next, err)
	}
}

func TestConsentRejectsBadInput(t *testing.T) {
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
	bad := consent("bank-a", "c1", 1, t0)
	bad.Status = "Maybe"
	for _, c := range []struct {
		e    ConsentEvent
		want string
	}{
		{consent("", "c1", 1, t0), "a consent event needs a bank and a consent id"},
		{consent("bank-a", "c1", 1, time.Time{}), "consent c1 event needs a changed time"},
		{consent("bank-a", "c1", 0, t0), "consent c1 event: source seq 0 must be 1 or more"},
		{bad, `consent c1 event: status "Maybe" must be AwaitingAuthorisation, Authorised, Rejected, Revoked, Expired, Dormant, ReconsentRequired`},
	} {
		_, _, err := s.RecordConsent(ctx, c.e)
		wantErr(t, err, c.want)
	}
}
