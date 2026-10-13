package home

import (
	"reflect"
	"testing"
)

func TestMergeExcludedModelListsUnionsPersistedAndGlobal(t *testing.T) {
	got := mergeExcludedModelLists([]string{"chat_*", "tab_*"}, []string{"chat_*", "tab_*", "gemini-3.5-flash-low", " "})
	want := []string{"chat_*", "tab_*", "gemini-3.5-flash-low"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if got := mergeExcludedModelLists(nil, nil); len(got) != 0 {
		t.Fatalf("want empty, got %v", got)
	}
}
