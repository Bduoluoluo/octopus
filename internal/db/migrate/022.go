package migrate

import (
	"github.com/xuanli27/octopus/internal/model"
	"gorm.io/gorm"
)

func init() {
	RegisterBeforeAutoMigration(Migration{Version: 22, Up: migrateChannelPromptSuffix})
}

func migrateChannelPromptSuffix(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable(&model.Channel{}) || db.Migrator().HasColumn(&model.Channel{}, "prompt_suffix") {
		return nil
	}
	return db.Migrator().AddColumn(&model.Channel{}, "PromptSuffix")
}
