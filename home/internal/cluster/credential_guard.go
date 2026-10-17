package cluster

import (
	"encoding/json"
	"errors"
	"strings"
)

// ErrAuthCredentialLoss rejects an update that would drop credential secrets the
// stored row still has (e.g. a light runtime copy persisted over a full row).
var ErrAuthCredentialLoss = errors.New("refusing auth update that drops stored credential secrets")

// guardedCredentialKeys are metadata keys whose presence in the stored row must
// never be erased by an update. Rotation (new non-empty value) is allowed.
var guardedCredentialKeys = []string{"refresh_token", "access_token", "id_token", "api_key"}

// credentialSecretsLost returns the first guarded key that is non-empty in the
// stored auth JSON but missing/empty in the next auth JSON, or "".
func credentialSecretsLost(existingJSON, nextJSON string) string {
	prev, prevKind := authMetadataStrings(existingJSON)
	if len(prev) == 0 {
		return ""
	}
	next, nextKind := authMetadataStrings(nextJSON)
	// A deliberate credential-kind change (e.g. OAuth -> API key, or a provider
	// change) legitimately replaces the secret set; light copies never do this.
	if nextKind.apiKey || nextKind.provider != prevKind.provider {
		return ""
	}
	for _, k := range guardedCredentialKeys {
		if prev[k] != "" && next[k] == "" {
			return k
		}
	}
	return ""
}

type credentialKind struct {
	provider string
	apiKey   bool
}

func authMetadataStrings(raw string) (map[string]string, credentialKind) {
	var doc struct {
		Provider   string            `json:"provider"`
		Attributes map[string]string `json:"attributes"`
		Metadata   map[string]any    `json:"metadata"`
	}
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &doc) != nil {
		return nil, credentialKind{}
	}
	kind := credentialKind{
		provider: strings.ToLower(strings.TrimSpace(doc.Provider)),
		apiKey:   strings.TrimSpace(doc.Attributes["api_key"]) != "" || strings.EqualFold(strings.TrimSpace(doc.Attributes["auth_kind"]), "apikey"),
	}
	out := make(map[string]string, len(guardedCredentialKeys))
	for _, k := range guardedCredentialKeys {
		if v, ok := doc.Metadata[k].(string); ok {
			out[k] = strings.TrimSpace(v)
		}
	}
	return out, kind
}
