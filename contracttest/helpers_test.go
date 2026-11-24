package contracttest

import (
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/pb33f/libopenapi"
	"github.com/pb33f/libopenapi-validator/schema_validation"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"

	"github.com/evolutionlabs-core/beerus/internal/config"
)

const (
	waitLimit = 60 * time.Second
	waitPoll  = 20 * time.Millisecond
)

func waitFor(t *testing.T, limit time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(waitPoll)
	defer tick.Stop()
	for !cond() {
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", what)
		case <-tick.C:
		}
	}
}

func accountAt(t *testing.T, c connected, typ string) map[string]any {
	t.Helper()
	for _, a := range c.Accounts {
		if a["type"] == typ {
			return a
		}
	}
	t.Fatalf("no %s account", typ)
	return nil
}

func rawGet(s *Stack, clientID, secret, token, path string) int {
	req, _ := http.NewRequest("GET", s.cfg.PartnerURL()+path, nil)
	req.Header.Set(config.ClientIDHeader, clientID)
	req.Header.Set(config.SecretHeader, secret)
	if token != "" {
		req.Header.Set(config.AccessTokenHeader, token)
	}
	resp, err := s.raw.Do(req)
	if err != nil {
		return -1
	}
	resp.Body.Close()
	return resp.StatusCode
}

func asList(v any) []map[string]any {
	out := []map[string]any{}
	items, _ := v.([]any)
	for _, x := range items {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func (s *Stack) status(token string) any {
	s.t.Helper()
	return s.must(s.call("GET", "/v1/connection", nil, withToken(token)), 200).JSON["status"]
}

func (s *Stack) id(c connected, typ string) string {
	return fmt.Sprint(accountAt(s.t, c, typ)["id"])
}

func (s *Stack) run(name string, f func(s *Stack)) {
	s.t.Helper()
	s.t.Run(name, func(t *testing.T) { f(s.sub(t)) })
}

var partnerModel = sync.OnceValues(func() (*v3.Document, error) {
	spec, err := os.ReadFile(specPath())
	if err != nil {
		return nil, err
	}
	doc, err := libopenapi.NewDocument(spec)
	if err != nil {
		return nil, err
	}
	model, err := doc.BuildV3Model()
	if err != nil {
		return nil, err
	}
	return &model.Model, nil
})

func validateWebhook(t *testing.T, typ string, body []byte) {
	t.Helper()
	model, err := partnerModel()
	if err != nil {
		t.Fatal(err)
	}
	item, ok := model.Webhooks.Get(typ)
	if !ok || item.Post == nil {
		t.Fatalf("webhook %s is not in the spec", typ)
	}
	media, ok := item.Post.RequestBody.Content.Get("application/json")
	if !ok {
		t.Fatalf("webhook %s has no JSON body in the spec", typ)
	}
	if ok, errs := schema_validation.NewSchemaValidator().ValidateSchemaBytes(media.Schema.Schema(), body); !ok {
		for _, e := range errs {
			t.Errorf("webhook %s: %s %s", typ, e.Message, e.Reason)
		}
	}
}

func (s *Stack) payListed(fromBank, from, toBank, to, naira string) {
	s.t.Helper()
	waitFor(s.t, waitLimit, "the sending bank to list "+toBank+" in the directory", func() bool {
		_, err := s.payTry(fromBank, from, toBank, to, naira, nil)
		return err == nil
	})
}
