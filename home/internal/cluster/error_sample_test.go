package cluster

import (
	"context"
	"fmt"
	"testing"
)

func TestErrorSamplePruning(t *testing.T) {
	ctx := context.Background()
	repo, closeRepo := newBillingTestRepository(t, ctx)
	defer closeRepo()
	for typ := 0; typ < 4; typ++ {
		for i := 0; i < 7; i++ {
			rec := &ErrorSampleRecord{Type: fmt.Sprintf("400:t%d", typ), Status: 400}
			if err := repo.InsertErrorSample(ctx, rec, []byte(`{"a":1}`), 5, 3); err != nil {
				t.Fatal(err)
			}
		}
	}
	types, err := repo.ListErrorSampleTypes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(types) != 3 || types[0].Type != "400:t3" || types[0].Count != 5 {
		t.Fatalf("unexpected %+v", types)
	}
	rows, _ := repo.ListErrorSamples(ctx, "400:t3")
	raw, err := DecodeErrorSample(rows[0].Data)
	if err != nil || string(raw) != `{"a":1}` {
		t.Fatalf("decode %s %v", raw, err)
	}
}
