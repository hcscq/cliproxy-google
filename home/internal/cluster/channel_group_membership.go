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
	active := db.Model(&AuthRecord{}).Select("uuid")
	result := db.WithContext(contextOrBackground(ctx)).Where("auth_id NOT IN (?)", active).Delete(&ChannelGroupDetailRecord{})
	return result.RowsAffected, result.Error
}
