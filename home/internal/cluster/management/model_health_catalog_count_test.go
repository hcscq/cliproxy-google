package management

import (
	"testing"
	"time"
)

func TestCountModelCredentialsPartialCatalog(t *testing.T) {
	future := time.Now().Add(time.Minute)
	var all []modelHealthCredential
	for i := 0; i < 15; i++ {
		all = append(all, modelHealthCredential{InCatalog: true, State: "ok"})
	}
	all = append(all, modelHealthCredential{InCatalog: true, State: "cooling", NextRetryAfter: &future})
	all[0].State = "error" // e.g. 499 still usable
	for i := 0; i < 36; i++ { // not offered by these accounts, one with a stale cooldown
		all = append(all, modelHealthCredential{State: "ok"})
	}
	all[20].State, all[20].NextRetryAfter = "cooling", &future
	c := countModelCredentials(all, true)
	if c.total != 16 || c.available != 15 || c.cooling != 1 || len(c.creds) != 16 {
		t.Fatalf("got total=%d avail=%d cool=%d creds=%d", c.total, c.available, c.cooling, len(c.creds))
	}
	if c.earliestRetry == nil {
		t.Fatal("earliest retry from in-catalog cooling expected")
	}
}

func TestCountModelCredentialsNoCatalog(t *testing.T) {
	all := []modelHealthCredential{{State: "ok"}, {State: "ok"}, {State: "removed"}}
	c := countModelCredentials(all, false)
	if c.total != 2 || c.available != 2 || c.removed != 1 || len(c.creds) != 3 {
		t.Fatalf("%+v", c)
	}
}

func TestCountModelCredentialsStaleCooldownOnNonCatalogIgnored(t *testing.T) {
	future := time.Now().Add(time.Hour)
	all := []modelHealthCredential{{InCatalog: true, State: "ok"}, {State: "cooling", NextRetryAfter: &future}}
	c := countModelCredentials(all, true)
	if c.total != 1 || c.cooling != 0 || c.earliestRetry != nil {
		t.Fatalf("%+v", c)
	}
}
