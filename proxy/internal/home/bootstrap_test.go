package home

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestConfigFromBootstrapEnrollsWithFreshIdentity(t *testing.T) {
	caKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	caDER, _ := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ca"}, NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}, &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ca"}}, &caKey.PublicKey, caKey)
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	gotArgs := make(chan []string, 1)
	go func() {
		conn, errAccept := ln.Accept()
		if errAccept != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		line, _ := r.ReadString('\n')
		var n int
		fmt.Sscanf(line, "*%d", &n)
		args := make([]string, 0, n)
		for i := 0; i < n; i++ {
			l, _ := r.ReadString('\n')
			var size int
			fmt.Sscanf(l, "$%d", &size)
			buf := make([]byte, size+2)
			_, _ = r.Read(buf[:0])
			for read := 0; read < size+2; {
				m, _ := r.Read(buf[read:])
				read += m
			}
			args = append(args, string(buf[:size]))
		}
		gotArgs <- args
		payload, _ := json.Marshal(bootstrapResponse{OK: true, CertificateID: "node-123", Certificate: caPEM, CA: caPEM})
		fmt.Fprintf(conn, "$%d\r\n%s\r\n", len(payload), payload)
	}()

	dir := t.TempDir()
	t.Setenv("HOME_CERT_DIR", dir)
	t.Setenv(BootstrapTokenEnv, "tok")
	t.Setenv(BootstrapAddressEnv, ln.Addr().String())
	t.Setenv(BootstrapNodeNameEnv, "cliproxy-abc")
	fp, _ := certificateFingerprintPEM([]byte(caPEM))
	t.Setenv(BootstrapCAFingerprint, fp)

	cfg, err := ConfigFromBootstrap(context.Background())
	if err != nil {
		t.Fatalf("ConfigFromBootstrap() error = %v", err)
	}
	args := <-gotArgs
	if len(args) != 5 || args[0] != "CERTIFICATE" || args[1] != "BOOTSTRAP" || args[2] != "tok" || args[3] != "cliproxy-abc" || !strings.Contains(args[4], "CERTIFICATE REQUEST") {
		t.Fatalf("unexpected RESP args: %q", args[:4])
	}
	if cfg.NodeID != "node-123" || !cfg.TLS.Enable || cfg.Host != "127.0.0.1" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	for _, p := range []string{cfg.TLS.ClientCert, cfg.TLS.ClientKey, cfg.TLS.CACert} {
		if _, errStat := os.Stat(p); errStat != nil {
			t.Fatalf("missing %s: %v", p, errStat)
		}
	}
}

func TestBootstrapConfiguredRequiresToken(t *testing.T) {
	t.Setenv(BootstrapTokenEnv, "")
	if BootstrapConfigured() {
		t.Fatal("bootstrap must be disabled without token")
	}
}
