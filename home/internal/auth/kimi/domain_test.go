package kimi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type kimiDomainTransport func(*http.Request) (*http.Response, error)

func (f kimiDomainTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestKimiDomainsUseIndependentOAuthEndpoints(t *testing.T) {
	for _, tc := range []struct{ domain, host string }{{KimiDefaultDomain, "auth.kimi.com"}, {KimiAIDomain, "auth.kimi.ai"}} {
		t.Run(tc.domain, func(t *testing.T) {
			client := NewDeviceFlowClientWithDeviceIDAndProxyURL(nil, "test-device", "", tc.domain)
			calls := []string{}
			client.httpClient.Transport = kimiDomainTransport(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host != tc.host {
					t.Errorf("OAuth host = %s, want %s", req.URL.Host, tc.host)
				}
				if err := req.ParseForm(); err != nil {
					t.Fatal(err)
				}
				if req.Form.Get("client_id") != kimiClientID {
					t.Error("client id missing")
				}
				calls = append(calls, req.URL.Path+":"+req.Form.Get("grant_type"))
				body := `{"access_token":"fixture-access","refresh_token":"fixture-refresh","expires_in":3600,"token_type":"Bearer"}`
				if strings.HasSuffix(req.URL.Path, "device_authorization") {
					body = `{"device_code":"device","user_code":"code","verification_uri":"https://example.test/verify"}`
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			device, err := client.RequestDeviceCode(context.Background())
			if err != nil || device.DeviceCode != "device" {
				t.Fatalf("device code: %v %v", device, err)
			}
			token, errExchange, more := client.exchangeDeviceCode(context.Background(), device.DeviceCode)
			if errExchange != nil || more || token.AccessToken != "fixture-access" {
				t.Fatalf("exchange: %v %v %v", token, errExchange, more)
			}
			token, err = client.RefreshToken(context.Background(), "fixture-refresh")
			if err != nil || token.AccessToken != "fixture-access" {
				t.Fatalf("refresh: %v %v", token, err)
			}
			if len(calls) != 3 || calls[2] != "/api/oauth/token:refresh_token" {
				t.Fatalf("calls=%v", calls)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, errCancelled := client.PollForToken(ctx, device); errCancelled == nil {
				t.Fatal("cancelled poll returned a token")
			}
		})
	}
	defaultClient := NewKimiAuth(nil)
	if ResolveKimiOAuthHost(defaultClient.deviceClient.domain) != "https://auth.kimi.com" {
		t.Fatal("legacy default domain changed")
	}
}

func TestResolveKimiDomainPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, provider string
		attrs          map[string]string
		metadata       map[string]any
		want           string
	}{
		{"legacy", "kimi", nil, nil, KimiDefaultDomain},
		{"ai provider", "kimi-ai", nil, nil, KimiAIDomain},
		{"explicit domain", "kimi", nil, map[string]any{"domain": "kimi.ai"}, KimiAIDomain},
		{"API endpoint", "kimi", nil, map[string]any{"base_url": "https://api.kimi.ai/coding"}, KimiAIDomain},
		{"attribute precedence", "kimi-ai", map[string]string{"domain": "kimi.com"}, map[string]any{"domain": "kimi.ai"}, KimiDefaultDomain},
		{"foreign endpoint", "kimi", nil, map[string]any{"base_url": "https://kimi.ai.example.test/coding"}, KimiDefaultDomain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveKimiDomain(tc.provider, tc.attrs, tc.metadata); got != tc.want {
				t.Fatalf("domain=%s, want %s", got, tc.want)
			}
		})
	}
}
