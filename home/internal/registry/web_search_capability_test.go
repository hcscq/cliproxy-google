package registry

import (
	"encoding/json"
	"testing"
)

func TestNativeWebSearchCapabilityRefreshAndClone(t *testing.T) {
	var before, after ModelInfo
	if err := json.Unmarshal([]byte(`{"id":"search","native_capabilities":{"web_search":false}}`), &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"id":"search","native_capabilities":{"web_search":true}}`), &after); err != nil {
		t.Fatal(err)
	}
	if !modelSectionChanged([]*ModelInfo{&before}, []*ModelInfo{&after}) {
		t.Fatal("capability-only update not detected")
	}
	cloned := cloneModelInfo(&after)
	if cloned == nil || cloned.NativeCapabilities == nil || cloned.NativeCapabilities.WebSearch == nil {
		t.Fatalf("cloned capability is nil: %+v", cloned)
	}
	*cloned.NativeCapabilities.WebSearch = false
	if !*after.NativeCapabilities.WebSearch {
		t.Fatal("clone shares capability pointer")
	}
	raw, err := json.Marshal(&before)
	if err != nil {
		t.Fatal(err)
	}
	var restored ModelInfo
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.NativeCapabilities == nil || restored.NativeCapabilities.WebSearch == nil || *restored.NativeCapabilities.WebSearch {
		t.Fatal("explicit false lost")
	}
}

func TestNativeWebSearchCapabilityTriStateUnmarshalAndClone(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		wantPresent  bool
		wantVal      bool
		wantEmptyCap bool
	}{
		{
			name:        "true",
			raw:         `{"id":"m","native_capabilities":{"web_search":true}}`,
			wantPresent: true,
			wantVal:     true,
		},
		{
			name:        "false",
			raw:         `{"id":"m","native_capabilities":{"web_search":false}}`,
			wantPresent: true,
			wantVal:     false,
		},
		{
			name:        "absent",
			raw:         `{"id":"m"}`,
			wantPresent: false,
		},
		{
			name:         "empty_capabilities",
			raw:          `{"id":"m","native_capabilities":{}}`,
			wantPresent:  false,
			wantEmptyCap: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var model ModelInfo
			if err := json.Unmarshal([]byte(tc.raw), &model); err != nil {
				t.Fatal(err)
			}

			if tc.wantEmptyCap {
				if model.NativeCapabilities == nil || model.NativeCapabilities.WebSearch != nil {
					t.Fatalf("expected empty capabilities with nil web_search, got: %+v", model.NativeCapabilities)
				}
			} else if tc.wantPresent {
				if model.NativeCapabilities == nil || model.NativeCapabilities.WebSearch == nil {
					t.Fatal("expected non-nil web_search capability")
				}
				if *model.NativeCapabilities.WebSearch != tc.wantVal {
					t.Fatalf("expected web_search=%v, got %v", tc.wantVal, *model.NativeCapabilities.WebSearch)
				}
			} else {
				if model.NativeCapabilities != nil {
					t.Fatalf("expected nil NativeCapabilities, got: %+v", model.NativeCapabilities)
				}
			}

			cloned := cloneModelInfo(&model)
			if tc.wantEmptyCap {
				if cloned.NativeCapabilities == nil || cloned.NativeCapabilities.WebSearch != nil {
					t.Fatalf("cloned empty capabilities mismatch: %+v", cloned.NativeCapabilities)
				}
				if cloned.NativeCapabilities == model.NativeCapabilities {
					t.Fatal("cloned empty capabilities shares struct pointer")
				}
			} else if tc.wantPresent {
				if cloned.NativeCapabilities == nil || cloned.NativeCapabilities.WebSearch == nil {
					t.Fatal("cloned expected non-nil web_search capability")
				}
				if cloned.NativeCapabilities == model.NativeCapabilities {
					t.Fatal("cloned shares NativeCapabilities struct pointer")
				}
				if cloned.NativeCapabilities.WebSearch == model.NativeCapabilities.WebSearch {
					t.Fatal("cloned shares WebSearch bool pointer")
				}
			} else {
				if cloned.NativeCapabilities != nil {
					t.Fatalf("cloned expected nil NativeCapabilities, got: %+v", cloned.NativeCapabilities)
				}
			}
		})
	}
}

func TestModelSectionChangedNativeCapabilities(t *testing.T) {
	var withTrue, withFalse, withNil ModelInfo
	if err := json.Unmarshal([]byte(`{"id":"search","native_capabilities":{"web_search":true}}`), &withTrue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"id":"search","native_capabilities":{"web_search":false}}`), &withFalse); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"id":"search"}`), &withNil); err != nil {
		t.Fatal(err)
	}

	if !modelSectionChanged([]*ModelInfo{&withNil}, []*ModelInfo{&withTrue}) {
		t.Fatal("nil vs true should be detected as changed")
	}
	if !modelSectionChanged([]*ModelInfo{&withNil}, []*ModelInfo{&withFalse}) {
		t.Fatal("nil vs false should be detected as changed")
	}
	if !modelSectionChanged([]*ModelInfo{&withTrue}, []*ModelInfo{&withFalse}) {
		t.Fatal("true vs false should be detected as changed")
	}
	if modelSectionChanged([]*ModelInfo{&withTrue}, []*ModelInfo{&withTrue}) {
		t.Fatal("identical true should not be detected as changed")
	}
	if modelSectionChanged([]*ModelInfo{&withNil}, []*ModelInfo{&withNil}) {
		t.Fatal("identical nil should not be detected as changed")
	}
}
