package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

const consentsPath = "/v1/consents"

type consentBody struct {
	Incarnation string    `json:"incarnation"`
	SourceSeq   int64     `json:"source_seq"`
	ConsentID   string    `json:"consent_id"`
	ClientID    string    `json:"client_id"`
	PartnerID   string    `json:"partner_id"`
	Status      string    `json:"status"`
	Via         string    `json:"via"`
	ChangedAt   time.Time `json:"changed_at"`
}

func (c NIBSSClient) RecordConsent(ctx context.Context, incarnation string, e openbanking.ConsentEvent) error {
	body := consentBody{Incarnation: incarnation, SourceSeq: e.Seq, ConsentID: e.ConsentID, ClientID: e.ClientID, PartnerID: e.PartnerID, Status: string(e.Status), Via: e.Via, ChangedAt: e.At}
	if _, err := c.Do(ctx, http.MethodPost, consentsPath, nil, body, nil); err != nil {
		return fmt.Errorf("record consent %s at NIBSS: %w", e.ConsentID, err)
	}
	return nil
}
