package partner_test

import (
	"os"
	"testing"

	"github.com/pb33f/libopenapi"
	validator "github.com/pb33f/libopenapi-validator"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
)

const specFile = "openapi.yaml"

func hasExample(t *testing.T, mt *v3.MediaType) bool {
	t.Helper()
	if mt.Example != nil {
		return true
	}
	if mt.Schema == nil {
		return false
	}
	s := mt.Schema.Schema()
	if s == nil {
		t.Fatal("schema does not resolve")
	}
	return s.Example != nil || len(s.Examples) > 0
}

func checkOperation(t *testing.T, name string, op *v3.Operation) {
	t.Helper()
	if op.OperationId == "" {
		t.Errorf("%s has no operation ID", name)
	}
	if op.RequestBody != nil {
		for c := op.RequestBody.Content.First(); c != nil; c = c.Next() {
			if !hasExample(t, c.Value()) {
				t.Errorf("%s: request body %s has no example", op.OperationId, c.Key())
			}
		}
	}
	for r := op.Responses.Codes.First(); r != nil; r = r.Next() {
		if r.Value().Content == nil {
			continue
		}
		for c := r.Value().Content.First(); c != nil; c = c.Next() {
			if !hasExample(t, c.Value()) {
				t.Errorf("%s: response %s %s has no example", op.OperationId, r.Key(), c.Key())
			}
		}
	}
}

func TestSpecParsesWithExamples(t *testing.T) {
	spec, err := os.ReadFile(specFile)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := libopenapi.NewDocument(spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, errs := validator.NewValidator(doc); len(errs) > 0 {
		t.Fatalf("validator: %v", errs)
	}
	model, err := doc.BuildV3Model()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for p := model.Model.Paths.PathItems.First(); p != nil; p = p.Next() {
		for o := p.Value().GetOperations().First(); o != nil; o = o.Next() {
			count++
			checkOperation(t, o.Key()+" "+p.Key(), o.Value())
		}
	}
	for w := model.Model.Webhooks.First(); w != nil; w = w.Next() {
		for o := w.Value().GetOperations().First(); o != nil; o = o.Next() {
			count++
			checkOperation(t, w.Key(), o.Value())
		}
	}
	if count == 0 {
		t.Fatal("the spec has no operations")
	}
}
