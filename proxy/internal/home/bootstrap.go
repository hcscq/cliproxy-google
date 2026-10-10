package home

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// Bootstrap environment variables used by stateless (Deployment) replicas.
const (
	BootstrapTokenEnv      = "HOME_BOOTSTRAP_TOKEN"
	BootstrapAddressEnv    = "HOME_ADDRESS"
	BootstrapCAFingerprint = "HOME_CA_FINGERPRINT"
	BootstrapNodeNameEnv   = "HOME_NODE_NAME"
)

type bootstrapResponse struct {
	OK            bool   `json:"ok"`
	CertificateID string `json:"certificate_id"`
	Certificate   string `json:"certificate"`
	CA            string `json:"ca"`
}

// BootstrapConfigured reports whether bootstrap-token self-enrollment is configured.
func BootstrapConfigured() bool {
	return strings.TrimSpace(os.Getenv(BootstrapTokenEnv)) != "" && strings.TrimSpace(os.Getenv(BootstrapAddressEnv)) != ""
}

// ConfigFromBootstrap self-enrolls with Home using a reusable bootstrap token.
// A fresh RSA key and certificate are generated for every process start; they
// are written only to HOME_CERT_DIR (intended to be an in-memory tmpfs) because
// the TLS stack loads them by path. Nothing is persisted across restarts.
func ConfigFromBootstrap(ctx context.Context) (config.HomeConfig, error) {
	token := strings.TrimSpace(os.Getenv(BootstrapTokenEnv))
	address := strings.TrimSpace(os.Getenv(BootstrapAddressEnv))
	if token == "" || address == "" {
		return config.HomeConfig{}, fmt.Errorf("%s and %s are required", BootstrapTokenEnv, BootstrapAddressEnv)
	}
	host, portRaw, errSplit := net.SplitHostPort(address)
	if errSplit != nil {
		return config.HomeConfig{}, fmt.Errorf("invalid %s: %w", BootstrapAddressEnv, errSplit)
	}
	port, errPort := strconv.Atoi(portRaw)
	if errPort != nil || port <= 0 {
		return config.HomeConfig{}, fmt.Errorf("invalid %s port", BootstrapAddressEnv)
	}
	nodeName := strings.TrimSpace(os.Getenv(BootstrapNodeNameEnv))
	if nodeName == "" {
		nodeName, _ = os.Hostname()
	}
	paths, errPaths := bootstrapCertificatePaths()
	if errPaths != nil {
		return config.HomeConfig{}, errPaths
	}
	key, errKey := rsa.GenerateKey(rand.Reader, 2048)
	if errKey != nil {
		return config.HomeConfig{}, errKey
	}
	csrPEM, errCSR := createClientCSR(nodeName, key)
	if errCSR != nil {
		return config.HomeConfig{}, errCSR
	}
	resp, errReq := requestBootstrapCertificate(ctx, address, token, nodeName, csrPEM)
	if errReq != nil {
		return config.HomeConfig{}, errReq
	}
	if resp.CertificateID == "" || strings.TrimSpace(resp.Certificate) == "" || strings.TrimSpace(resp.CA) == "" {
		return config.HomeConfig{}, fmt.Errorf("home bootstrap response is incomplete")
	}
	if fp := strings.TrimSpace(os.Getenv(BootstrapCAFingerprint)); fp != "" {
		if errVerify := verifyCACertificatePEM([]byte(resp.CA), fp); errVerify != nil {
			return config.HomeConfig{}, errVerify
		}
	}
	if errMkdir := os.MkdirAll(paths.Dir, 0o700); errMkdir != nil {
		return config.HomeConfig{}, errMkdir
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	for path, raw := range map[string][]byte{paths.ClientKey: keyPEM, paths.ClientCert: []byte(resp.Certificate), paths.CACert: []byte(resp.CA)} {
		if errWrite := writeFile0600(path, raw); errWrite != nil {
			return config.HomeConfig{}, errWrite
		}
	}
	return config.HomeConfig{
		Enabled: true,
		NodeID:  resp.CertificateID,
		Host:    host,
		Port:    port,
		TLS: config.HomeTLSConfig{
			Enable:              true,
			CACert:              paths.CACert,
			ClientCert:          paths.ClientCert,
			ClientKey:           paths.ClientKey,
			UseTargetServerName: true,
		},
	}, nil
}

func bootstrapCertificatePaths() (certificatePaths, error) {
	dir := strings.TrimSpace(os.Getenv("HOME_CERT_DIR"))
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "cliproxy-home-identity")
	}
	dir = filepath.Clean(dir)
	return certificatePaths{
		Dir:        dir,
		ClientCert: filepath.Join(dir, "client-crt.pem"),
		ClientKey:  filepath.Join(dir, "client-key.pem"),
		CACert:     filepath.Join(dir, "home-ca-crt.pem"),
	}, nil
}

func requestBootstrapCertificate(ctx context.Context, address, token, nodeName string, csrPEM []byte) (bootstrapResponse, error) {
	var response bootstrapResponse
	if ctx == nil {
		ctx = context.Background()
	}
	dialCtx, cancel := context.WithTimeout(ctx, homeCertificateRequestTimeout)
	defer cancel()
	conn, errDial := (&net.Dialer{}).DialContext(dialCtx, "tcp", address)
	if errDial != nil {
		return response, errDial
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := dialCtx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if _, errWrite := conn.Write(encodeRESPArray("CERTIFICATE", "BOOTSTRAP", token, nodeName, string(csrPEM))); errWrite != nil {
		return response, errWrite
	}
	raw, errRead := readRESPBulk(bufio.NewReader(conn))
	if errRead != nil {
		return response, errRead
	}
	if errUnmarshal := json.Unmarshal(raw, &response); errUnmarshal != nil {
		return response, errUnmarshal
	}
	if !response.OK {
		return response, fmt.Errorf("home bootstrap enrollment failed")
	}
	return response, nil
}
