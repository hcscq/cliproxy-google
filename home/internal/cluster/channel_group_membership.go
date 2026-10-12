package cluster

import (
	"context"
	"errors"
	"os"
	"strings"

	"gorm.io/gorm"
)

// DefaultCredentialGroupEnv names the credential group new credentials join on add.
// Unset uses "default"; set to "" / "off" / "none" to disable auto-join.
const DefaultCredentialGroupEnv = "CREDENTIAL_DEFAULT_GROUP"

func defaultCredentialGroupName() string {
	value, ok := os.LookupEnv(DefaultCredentialGroupEnv)
	if !ok {
		return "default"
	}
	value = strings.TrimSpace(value)
	switch strings.ToLower(value) {
	case "", "off", "none", "false":
		return ""
	}
	return value
}

// autoJoinDefaultChannelGroup adds a newly created credential to the default credential
// group (if that group exists). Existing credentials (update / dedupe paths) are untouched.
func autoJoinDefaultChannelGroup(tx *gorm.DB, authUUID string) error {
	name := defaultCredentialGroupName()
	if name == "" || strings.TrimSpace(authUUID) == "" {
		return nil
	}
	group := ChannelGroupRecord{}
	if err := tx.Where("channel_name = ?", name).First(&group).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	var count int64
	if err := tx.Model(&ChannelGroupDetailRecord{}).Where("channel_group_id = ? AND auth_id = ?", group.ID, authUUID).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	return tx.Create(&ChannelGroupDetailRecord{ChannelGroupID: group.ID, AuthID: authUUID}).Error
}

// PurgeOrphanChannelGroupDetails removes group memberships whose credential no longer
// exists (or is soft-deleted). Returns the number of memberships removed.
func (r *Repository) PurgeOrphanChannelGroupDetails(ctx context.Context) (int64, error) {
	db, errDB := r.database()
	if errDB != nil {
		return 0, errDB
	}
	ctx = contextOrBackground(ctx)
	active := db.Model(&AuthRecord{}).Select("uuid")
	result := db.WithContext(ctx).Where("auth_id NOT IN (?)", active).Delete(&ChannelGroupDetailRecord{})
	if result.Error != nil {
		return 0, result.Error
	}
	removed := result.RowsAffected
	var gone []string
	if err := db.WithContext(ctx).Unscoped().Model(&AuthRecord{}).Where("deleted_at IS NOT NULL").Pluck("uuid", &gone).Error; err != nil {
		return removed, err
	}
	// Dependents keyed by credential id that point at no live credential.
	for _, dep := range credentialDependentTables {
		res := db.WithContext(ctx).Exec("DELETE FROM "+dep.table+" WHERE "+dep.column+" <> '' AND "+dep.column+" NOT IN (SELECT uuid FROM auth WHERE deleted_at IS NULL)")
		if res.Error != nil {
			return removed, res.Error
		}
		removed += res.RowsAffected
	}
	if len(gone) > 0 {
		if err := clearDeletedAuthModelStates(db.WithContext(ctx), gone); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

// credentialDependentTables hold per-credential runtime state that is meaningless once the
// credential is deleted (model catalogs, sticky-session bindings, concurrency state). The
// request_affinity outcome log is history and is left to its own retention.
var credentialDependentTables = []struct{ table, column string }{
	{"antigravity_catalogs", "auth_id"},
	{"session_affinity", "auth_id"},
	{"credential_concurrency_counters", "credential_id"},
	{"credential_concurrency_model_policies", "credential_id"},
	{"credential_concurrency_policies", "credential_id"},
}

// purgeCredentialDependents removes per-credential runtime rows and clears the stored
// per-model cooldown state of the given (being deleted) credentials.
func purgeCredentialDependents(tx *gorm.DB, uuids []string) error {
	if len(uuids) == 0 {
		return nil
	}
	for _, dep := range credentialDependentTables {
		if tx.Migrator().HasTable(dep.table) {
			if err := tx.Exec("DELETE FROM "+dep.table+" WHERE "+dep.column+" IN ?", uuids).Error; err != nil {
				return err
			}
		}
	}
	return clearDeletedAuthModelStates(tx, uuids)
}

// clearDeletedAuthModelStates drops model_states / cooldown fields from soft-deleted auth rows.
func clearDeletedAuthModelStates(tx *gorm.DB, uuids []string) error {
	if tx.Dialector == nil || tx.Dialector.Name() != "postgres" {
		return nil
	}
	return tx.Exec(`UPDATE auth SET auth_json = auth_json - 'model_states' - 'next_retry_after' - 'quota' - 'last_error' || '{"unavailable": false}'::jsonb
		WHERE uuid IN ? AND deleted_at IS NOT NULL AND (jsonb_exists(auth_json, 'model_states') OR (auth_json->>'unavailable')::boolean)`, uuids).Error
}
