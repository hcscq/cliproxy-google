package home

import (
	"errors"
	"testing"
	"time"
)

func TestCertificateRejectWatch(t *testing.T) {
	var w CertificateRejectWatch
	t0 := time.Unix(1000, 0)
	bad := errors.New("home cluster discovery transport failed: remote error: tls: bad certificate")
	if w.Observe(bad, t0, 30*time.Second) {
		t.Fatal("first rejection must not trigger")
	}
	if w.Observe(bad, t0.Add(29*time.Second), 30*time.Second) {
		t.Fatal("too early")
	}
	if !w.Observe(bad, t0.Add(31*time.Second), 30*time.Second) {
		t.Fatal("persistent rejection must trigger")
	}
	w.Observe(errors.New("EOF"), t0.Add(32*time.Second), 30*time.Second)
	if w.Observe(bad, t0.Add(40*time.Second), 30*time.Second) {
		t.Fatal("other errors reset the window")
	}
	if IsCertificateRejectedError(errors.New("read tcp: i/o timeout")) {
		t.Fatal("timeout is not a rejection")
	}
}
