package cluster

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// BootstrapTokenEnv names the environment variable holding the reusable
// bootstrap token that stateless proxy replicas use to self-enroll.
const BootstrapTokenEnv = "HOME_BOOTSTRAP_TOKEN"

// DefaultEphemeralNodeTTL is how long an ephemeral proxy identity survives
// without a heartbeat before it is garbage collected.
const DefaultEphemeralNodeTTL = 10 * time.Minute

type bootstrapCertificateResponse struct {
	OK            bool   `json:"ok"`
	CertificateID string `json:"certificate_id"`
	Certificate   string `json:"certificate"`
	CA            string `json:"ca"`
}

func bootstrapToken() string {
	return strings.TrimSpace(os.Getenv(BootstrapTokenEnv))
}

// BootstrapEnabled reports whether bootstrap-token enrollment is configured.
func BootstrapEnabled() bool { return bootstrapToken() != "" }

func verifyBootstrapToken(presented string) bool {
	expected := bootstrapToken()
	presented = strings.TrimSpace(presented)
	if expected == "" || presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(presented)) == 1
}

// BootstrapClientCertificate validates the shared bootstrap token, allocates a
// fresh ephemeral node identity and signs the CSR for it in one step.
func (r *Repository) BootstrapClientCertificate(ctx context.Context, token string, nodeName string, csrPEM []byte) ([]byte, error) {
	if !BootstrapEnabled() {
		return nil, fmt.Errorf("bootstrap enrollment is disabled")
	}
	if !verifyBootstrapToken(token) {
		return nil, fmt.Errorf("bootstrap token is invalid")
	}
	id, secret, errCreate := r.CreatePendingClientCertificate(ctx, nodeName)
	if errCreate != nil {
		return nil, errCreate
	}
	certPEM, caPEM, errSign := r.SignClientCertificateRequest(ctx, id, secret, csrPEM)
	if errSign != nil {
		return nil, errSign
	}
	db, errDB := r.database()
	if errDB != nil {
		return nil, errDB
	}
	if errMark := db.WithContext(contextOrBackground(ctx)).Model(&CertificateRecord{}).
		Where("id = ?", id).Update("ephemeral", true).Error; errMark != nil {
		return nil, errMark
	}
	log.Infof("bootstrap enrollment issued ephemeral identity %s (name=%q)", id, strings.TrimSpace(nodeName))
	return json.Marshal(bootstrapCertificateResponse{OK: true, CertificateID: id, Certificate: string(certPEM), CA: string(caPEM)})
}

// GarbageCollectEphemeralNodes revokes ephemeral proxy identities that have not
// heartbeated within ttl, and drops Home cluster rows that have been dead for
// longer than ttl (for example Deployment pods with changing IPs).
func (r *Repository) GarbageCollectEphemeralNodes(ctx context.Context, ttl time.Duration) (int64, error) {
	if ttl <= 0 {
		ttl = DefaultEphemeralNodeTTL
	}
	db, errDB := r.database()
	if errDB != nil {
		return 0, errDB
	}
	ctx = contextOrBackground(ctx)
	now := time.Now().UTC()
	if backendNow, errNow := DatabaseNow(ctx, db); errNow == nil && !backendNow.IsZero() {
		now = backendNow
	}
	cutoff := now.Add(-ttl)
	var removed int64
	errTx := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Compare timestamps in Go so the logic is identical on SQLite (string
		// timestamps) and PostgreSQL.
		var nodes []CPANodeRecord
		if errNodes := tx.Select("node_id", "last_seen_at").Find(&nodes).Error; errNodes != nil {
			return errNodes
		}
		liveIDs := make(map[string]struct{}, len(nodes))
		for _, n := range nodes {
			if !n.LastSeenAt.Before(cutoff) {
				liveIDs[strings.TrimSpace(n.NodeID)] = struct{}{}
			}
		}
		var certs []CertificateRecord
		if errFind := tx.Select("id", "created_at").Where("ephemeral = ?", true).Find(&certs).Error; errFind != nil {
			return errFind
		}
		var ids []string
		for _, c := range certs {
			if _, ok := liveIDs[c.ID]; ok || !c.CreatedAt.Before(cutoff) {
				continue
			}
			ids = append(ids, c.ID)
		}
		if len(ids) > 0 {
			if errDel := tx.Where("node_id IN ?", ids).Delete(&CPANodeMetadataRecord{}).Error; errDel != nil {
				return errDel
			}
			res := tx.Where("id IN ?", ids).Delete(&CertificateRecord{})
			if res.Error != nil {
				return res.Error
			}
			removed = res.RowsAffected
		}
		var homes []ClusterNodeRecord
		if errHomes := tx.Find(&homes).Error; errHomes != nil {
			return errHomes
		}
		usedIPs := map[string]struct{}{}
		for _, h := range homes {
			if !h.IsMaster && h.LastSeenAt.Before(cutoff) {
				if errDel := tx.Where("ip = ? AND port = ? AND is_master = ?", h.IP, h.Port, false).Delete(&ClusterNodeRecord{}).Error; errDel != nil {
					return errDel
				}
				continue
			}
			usedIPs[h.IP] = struct{}{}
		}
		for _, h := range homes {
			if _, used := usedIPs[h.IP]; used || h.IsMaster || !h.LastSeenAt.Before(cutoff) {
				continue
			}
			// Server certificates of dead Home pods (pod IPs are not stable).
			if errDel := tx.Where("is_server = ? AND ip = ?", true, h.IP).Delete(&CertificateRecord{}).Error; errDel != nil {
				return errDel
			}
		}
		return nil
	})
	return removed, errTx
}

// EphemeralNodeTTLFromEnv reads HOME_EPHEMERAL_NODE_TTL (Go duration).
func EphemeralNodeTTLFromEnv() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("HOME_EPHEMERAL_NODE_TTL")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	return DefaultEphemeralNodeTTL
}

var ephemeralColumnOnce sync.Map // *gorm.DB -> struct{}

// ensureEphemeralColumn adds certificate.ephemeral on PostgreSQL databases whose
// schema version was already current before the column existed (the versioned
// migrator skips AutoMigrate in that case). It is idempotent and cheap.
func ensureEphemeralColumn(ctx context.Context, db *gorm.DB) error {
	if db == nil || db.Dialector == nil || db.Dialector.Name() != "postgres" {
		return nil
	}
	if _, done := ephemeralColumnOnce.Load(db); done {
		return nil
	}
	if err := db.WithContext(contextOrBackground(ctx)).Exec(`ALTER TABLE certificate ADD COLUMN IF NOT EXISTS ephemeral boolean NOT NULL DEFAULT false`).Error; err != nil {
		return err
	}
	if err := db.WithContext(contextOrBackground(ctx)).Exec(`CREATE INDEX IF NOT EXISTS idx_certificate_ephemeral ON certificate (ephemeral)`).Error; err != nil {
		return err
	}
	ephemeralColumnOnce.Store(db, struct{}{})
	return nil
}
