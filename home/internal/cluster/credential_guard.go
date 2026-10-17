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
	prev := authMetadataStrings(existingJSON)
	if len(prev) == 0 {
		return ""
	}
	next := authMetadataStrings(nextJSON)
	for _, k := range guardedCredentialKeys {
		if prev[k] != "" && next[k] == "" {
			return k
		}
	}
	return ""
}

func authMetadataStrings(raw string) map[string]string {
	var doc struct {
		Metadata map[string]any `json:"metadata"`
	}
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &doc) != nil {
		return nil
	}
	out := make(map[string]string, len(guardedCredentialKeys))
	for _, k := range guardedCredentialKeys {
		if v, ok := doc.Metadata[k].(string); ok {
			out[k] = strings.TrimSpace(v)
		}
	}
	return out
}
