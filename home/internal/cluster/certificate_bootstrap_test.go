package cluster

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// Homes starting together on an empty database must converge on one CA.
func TestEnsureClusterCertificatesConcurrentBootstrapCreatesOneCA(t *testing.T) {
	db := openPartitionTestSchema(t)
	if err := db.AutoMigrate(&CertificateRecord{}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Two nodes share each IP, like several Homes behind one address.
			_, err := NewRepository(db).EnsureClusterCertificates(context.Background(), fmt.Sprintf("10.9.0.%d", i%4))
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var cas, servers int64
	db.Model(&CertificateRecord{}).Where("is_ca = ?", true).Count(&cas)
	db.Model(&CertificateRecord{}).Where("is_server = ?", true).Count(&servers)
	if cas != 1 || servers != 4 {
		t.Fatalf("got %d CA and %d server certificates, want 1 and 4", cas, servers)
	}
}
