package cluster

import (
	"context"
	"testing"
	"time"
)

func TestCredentialHasConcurrencyLimit(t *testing.T) {
	ctx := context.Background()
	db, err := OpenSQLite(ctx, t.TempDir()+"/c.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&CredentialConcurrencyPolicyRecord{}, &CredentialConcurrencyModelPolicyRecord{}); err != nil {
		t.Fatal(err)
	}
	check := func(cred, model string, want bool) {
		t.Helper()
		got, err := credentialHasConcurrencyLimit(ctx, db, cred, model)
		if err != nil || got != want {
			t.Fatalf("%s/%s limited=%v err=%v want %v", cred, model, got, err, want)
		}
	}
	check("none", "m", false) // no policy row: fast path
	zero, five := int64(0), int64(5)
	now := time.Now()
	db.Create(&CredentialConcurrencyPolicyRecord{CredentialID: "zero", MaxInFlight: &zero, Version: 1, EffectiveAt: now})
	check("zero", "m", false) // policy row without a positive limit
	db.Create(&CredentialConcurrencyPolicyRecord{CredentialID: "cred", MaxInFlight: &five, Version: 1, EffectiveAt: now})
	check("cred", "anything", true)
	db.Create(&CredentialConcurrencyPolicyRecord{CredentialID: "model", Version: 1, EffectiveAt: now})
	db.Create(&CredentialConcurrencyModelPolicyRecord{CredentialID: "model", Model: "m1", MaxInFlight: 2})
	check("model", "m1", true)
	check("model", "m2", false)
	check("", "m", true) // invalid input goes to the locked path for proper errors
}
