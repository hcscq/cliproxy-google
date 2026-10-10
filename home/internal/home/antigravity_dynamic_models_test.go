package home

import "testing"

func TestMergeAntigravityModelsAddsUpstreamOnlyModels(t *testing.T) {
	static := []*ModelInfo{{ID: "gemini-3-pro", DisplayName: "Gemini 3 Pro", Type: "antigravity"}, {ID: "retired-model"}}
	upstream, err := parseAntigravityCatalog([]byte(`{"models":{"gemini-3-pro":{"displayName":"x"},"gemini-3.5-flash":{"displayName":"Gemini 3.5 Flash","maxTokens":1048576,"maxOutputTokens":65536}}}`))
	if err != nil {
		t.Fatal(err)
	}
	got := mergeAntigravityModels(static, upstream, []*ModelInfo{{ID: "cfg-model"}}, []string{"extra-model", "gemini-3-pro"})
	ids := map[string]*ModelInfo{}
	for _, m := range got {
		ids[m.ID] = m
	}
	for _, want := range []string{"gemini-3-pro", "gemini-3.5-flash", "cfg-model", "extra-model"} {
		if ids[want] == nil {
			t.Fatalf("missing %s in %v", want, ids)
		}
	}
	if ids["retired-model"] != nil {
		t.Fatal("static model not offered upstream must be filtered")
	}
	if ids["gemini-3-pro"].DisplayName != "Gemini 3 Pro" {
		t.Fatal("static metadata should win")
	}
	if m := ids["gemini-3.5-flash"]; m.DisplayName != "Gemini 3.5 Flash" || m.ContextLength != 1048576 || m.OutputTokenLimit != 65536 {
		t.Fatalf("bad synthesized model %+v", m)
	}
	if len(got) != 4 {
		t.Fatalf("duplicates: %d", len(got))
	}
}

func TestMergeAntigravityModelsWithoutCatalogKeepsStatic(t *testing.T) {
	got := mergeAntigravityModels([]*ModelInfo{{ID: "a"}, {ID: "b"}}, nil, nil, nil)
	if len(got) != 2 {
		t.Fatalf("got %d", len(got))
	}
}
