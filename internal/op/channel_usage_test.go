package op

import (
	"context"
	"testing"

	"github.com/xuanli27/octopus/internal/db"
	"github.com/xuanli27/octopus/internal/model"
	"gorm.io/gorm"
)

func setupChannelUsageTest(t *testing.T) (context.Context, *model.Channel) {
	t.Helper()
	ctx := setupSiteOpTestDB(t)
	if err := channelRefreshCache(ctx); err != nil {
		t.Fatal(err)
	}
	channel := &model.Channel{
		Name: "usage-test", Enabled: true,
		Keys: []model.ChannelKey{
			{Enabled: true, ChannelKey: "first-key", TotalCost: 2},
			{Enabled: true, ChannelKey: "second-key", TotalCost: 4},
		},
	}
	if err := ChannelCreate(channel, ctx); err != nil {
		t.Fatal(err)
	}
	for _, key := range channel.Keys {
		key.TotalCost += 10
		key.StatusCode = 200
		key.LastUseTimeStamp = 1234567890
		if err := ChannelKeyUpdate(key); err != nil {
			t.Fatal(err)
		}
	}
	return ctx, channel
}

func TestChannelEditPreservesPendingKeyUsage(t *testing.T) {
	for _, operation := range []string{"rename", "key settings", "add and delete", "lookup by name"} {
		t.Run(operation, func(t *testing.T) {
			ctx, channel := setupChannelUsageTest(t)
			request := &model.ChannelUpdateRequest{ID: channel.ID}
			name, remark, credential, enabled := "renamed", "updated remark", "updated-key", false
			switch operation {
			case "rename":
				request.Name = &name
			case "key settings":
				request.KeysToUpdate = []model.ChannelKeyUpdateRequest{{ID: channel.Keys[0].ID, Remark: &remark, ChannelKey: &credential, Enabled: &enabled}}
			case "add and delete":
				request.KeysToDelete = []int{channel.Keys[1].ID}
				request.KeysToAdd = []model.ChannelKeyAddRequest{{Enabled: true, ChannelKey: "new-key"}}
			}
			var updated *model.Channel
			var err error
			if operation == "lookup by name" {
				updated, err = ChannelGetByName(channel.Name, ctx)
			} else {
				updated, err = ChannelUpdate(request, ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range updated.Keys {
				wantCost := 0.0
				for _, original := range channel.Keys {
					if original.ID == key.ID {
						wantCost = original.TotalCost + 10
						if key.StatusCode != 200 || key.LastUseTimeStamp != 1234567890 {
							t.Fatalf("runtime state lost: %+v", key)
						}
					}
				}
				if key.TotalCost != wantCost {
					t.Fatalf("key %d cost=%v, want %v", key.ID, key.TotalCost, wantCost)
				}
				if operation == "key settings" && key.ID == channel.Keys[0].ID && (key.Remark != remark || key.ChannelKey != credential || key.Enabled) {
					t.Fatalf("key settings not applied: %+v", key)
				}
			}
			if err := ChannelKeySaveDB(ctx); err != nil {
				t.Fatal(err)
			}
			if err := channelRefreshCache(ctx); err != nil {
				t.Fatal(err)
			}
			reloaded, err := ChannelGet(channel.ID, ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(reloaded.Keys) != len(updated.Keys) {
				t.Fatalf("keys changed after saving usage: %+v", reloaded.Keys)
			}
			for index, key := range updated.Keys {
				if reloaded.Keys[index] != key {
					t.Fatalf("usage/configuration not persisted: got %+v, want %+v", reloaded.Keys[index], key)
				}
			}
		})
	}
}

func TestChannelKeyLateResultPreservesEditedSettings(t *testing.T) {
	ctx, channel := setupChannelUsageTest(t)
	stale, _ := channelKeyCache.Get(channel.Keys[0].ID)
	remark, enabled := "edited while request running", false
	if _, err := ChannelUpdate(&model.ChannelUpdateRequest{ID: channel.ID, KeysToUpdate: []model.ChannelKeyUpdateRequest{{ID: stale.ID, Remark: &remark, Enabled: &enabled}}}, ctx); err != nil {
		t.Fatal(err)
	}
	stale.TotalCost += 3
	if err := ChannelKeyUpdate(stale); err != nil {
		t.Fatal(err)
	}
	if err := ChannelKeySaveDB(ctx); err != nil {
		t.Fatal(err)
	}
	var saved model.ChannelKey
	if err := db.GetDB().First(&saved, stale.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Remark != remark || saved.Enabled || saved.TotalCost != 15 {
		t.Fatalf("late response reverted configuration or lost usage: %+v", saved)
	}
}

func TestChannelKeySaveDoesNotRestoreDeletedKey(t *testing.T) {
	ctx, channel := setupChannelUsageTest(t)
	key := channel.Keys[0]
	if err := db.GetDB().Delete(&model.ChannelKey{}, key.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := ChannelKeySaveDB(ctx); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.GetDB().Model(&model.ChannelKey{}).Where("id = ?", key.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("saving pending usage restored a deleted key")
	}
}

func TestChannelKeyUsageUpdateDuringSaveSurvivesRefresh(t *testing.T) {
	ctx, channel := setupChannelUsageTest(t)
	var stale model.Channel
	if err := db.GetDB().Preload("Keys").First(&stale, channel.ID).Error; err != nil {
		t.Fatal(err)
	}
	updatedDuringSave := false
	callbackName := "test:usage_during_save"
	if err := db.GetDB().Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if updatedDuringSave || tx.Statement.Table != "channel_keys" {
			return
		}
		updatedDuringSave = true
		key, _ := channelKeyCache.Get(channel.Keys[0].ID)
		key.TotalCost += 3
		if err := ChannelKeyUpdate(key); err != nil {
			t.Error(err)
		}
		cacheRefreshedChannel(&stale)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.GetDB().Callback().Update().Remove(callbackName) })
	if err := ChannelKeySaveDB(ctx); err != nil {
		t.Fatal(err)
	}
	if !updatedDuringSave {
		t.Fatal("save callback did not run")
	}
	cached, _ := channelKeyCache.Get(channel.Keys[0].ID)
	if cached.TotalCost != 15 {
		t.Fatalf("refresh lost in-flight usage: %+v", cached)
	}
	if err := ChannelKeySaveDB(ctx); err != nil {
		t.Fatal(err)
	}
	var saved model.ChannelKey
	if err := db.GetDB().First(&saved, cached.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.TotalCost != 15 {
		t.Fatalf("second save lost pending usage: %+v", saved)
	}
}

func TestChannelKeyFailedSaveCanRetryAfterEdit(t *testing.T) {
	ctx, channel := setupChannelUsageTest(t)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := ChannelKeySaveDB(canceled); err == nil {
		t.Fatal("expected canceled save to fail")
	}
	if err := ChannelEnabled(channel.ID, false, ctx); err != nil {
		t.Fatal(err)
	}
	if err := ChannelKeySaveDB(ctx); err != nil {
		t.Fatal(err)
	}
	var saved model.ChannelKey
	if err := db.GetDB().First(&saved, channel.Keys[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.TotalCost != 12 {
		t.Fatalf("retry lost pending usage: %+v", saved)
	}
}
