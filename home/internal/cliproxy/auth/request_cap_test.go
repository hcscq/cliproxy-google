package auth

import (
	"testing"
	"time"
)

type capGateRec struct{ ids []string }

func (g *capGateRec) OnDispatch(id string) { g.ids = append(g.ids, id) }

func TestRequestCapPredicate(t *testing.T) {
	pred := schedulerPredicate("", nil, nil, "", 0, 0)
	e := &scheduledAuth{auth: &Auth{ID: "x"}}
	if !pred(e) {
		t.Fatal("uncapped should pass")
	}
	MarkRequestCapped("x", time.Now().Add(time.Minute))
	if pred(e) {
		t.Fatal("capped should be skipped")
	}
	MarkRequestCapped("x", time.Time{})
	if !pred(e) || requestCapActive.Load() != 0 {
		t.Fatal("cleared")
	}
	MarkRequestCapped("y", time.Now().Add(-time.Second))
	if _, ok := RequestCappedUntil("y"); ok {
		t.Fatal("expired cap")
	}
	g := &capGateRec{}
	SetRequestCapGate(g)
	defer SetRequestCapGate(nil)
	notifyRequestCapDispatch("z")
	if len(g.ids) != 1 {
		t.Fatal("gate not notified")
	}
}
