package contracttest

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/pb33f/libopenapi"
	"github.com/pb33f/libopenapi-validator/schema_validation"
)

func jsonUnmarshal(b []byte, v any) error {
	return json.Unmarshal(b, v)
}

func validateWebhook(t *testing.T, typ string, body []byte) {
	t.Helper()
	spec, err := os.ReadFile(specPath())
	if err != nil {
		t.Fatal(err)
	}
	doc, err := libopenapi.NewDocument(spec)
	if err != nil {
		t.Fatal(err)
	}
	model, err := doc.BuildV3Model()
	if err != nil {
		t.Fatal(err)
	}
	item, ok := model.Model.Webhooks.Get(typ)
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
			for _, f := range e.SchemaValidationErrors {
				t.Errorf("  %s: %s", f.FieldPath, f.Reason)
			}
		}
	}
}

func testingExternal() bool {
	return os.Getenv("BEERUS_CONTRACT_DIR") != ""
}
