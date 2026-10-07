package migrate

import (
	"testing"

	"github.com/xuanli27/octopus/internal/model"
)

func TestMigrateChannelPromptSuffix(t *testing.T) {
	connection := openMigrationTestDB(t)
	if err := migrateChannelPromptSuffix(connection); err != nil {
		t.Fatal(err)
	}
	if err := connection.Exec(`CREATE TABLE channels (id INTEGER PRIMARY KEY, name TEXT)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := connection.Exec(`INSERT INTO channels (id, name) VALUES (7, 'legacy')`).Error; err != nil {
		t.Fatal(err)
	}
	if err := migrateChannelPromptSuffix(connection); err != nil {
		t.Fatal(err)
	}
	var channel model.Channel
	if err := connection.First(&channel, 7).Error; err != nil {
		t.Fatal(err)
	}
	if channel.Name != "legacy" || channel.PromptSuffix != nil {
		t.Fatalf("legacy channel changed: %+v", channel)
	}
	if err := connection.Model(&channel).Update("prompt_suffix", "\nkeep\n").Error; err != nil {
		t.Fatal(err)
	}
	if err := migrateChannelPromptSuffix(connection); err != nil {
		t.Fatal(err)
	}
	if err := connection.First(&channel, 7).Error; err != nil {
		t.Fatal(err)
	}
	if channel.PromptSuffix == nil || *channel.PromptSuffix != "\nkeep\n" {
		t.Fatalf("repeated migration lost suffix: %+v", channel)
	}
}
