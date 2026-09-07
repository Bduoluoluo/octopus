package op

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xuanli27/octopus/internal/db"
	"github.com/xuanli27/octopus/internal/model"
)

func setupLogStorageTest(test *testing.T, limitMB string) context.Context {
	test.Helper()
	ctx := setupSiteOpTestDB(test)
	resetRelayLogStateForTest()
	test.Cleanup(resetRelayLogStateForTest)
	if err := settingRefreshCache(ctx); err != nil {
		test.Fatal(err)
	}
	if err := SettingSetString(model.SettingKeyRelayLogMaxDBSizeMB, limitMB); err != nil {
		test.Fatal(err)
	}
	return ctx
}

func TestRelayLogCleanupWhenPersistenceDisabled(test *testing.T) {
	ctx := setupLogStorageTest(test, "1024")
	if err := SettingSetString(model.SettingKeyRelayLogKeepEnabled, "false"); err != nil {
		test.Fatal(err)
	}
	if err := SettingSetString(model.SettingKeyRelayLogKeepPeriod, "1"); err != nil {
		test.Fatal(err)
	}
	rows := []model.RelayLog{
		{ID: 1, Time: time.Now().Add(-25 * time.Hour).Unix()},
		{ID: 2, Time: time.Now().Unix()},
	}
	if err := db.GetDB().Create(&rows).Error; err != nil {
		test.Fatal(err)
	}
	if err := RelayLogSaveDBTask(ctx); err != nil {
		test.Fatal(err)
	}
	var remaining []int64
	if err := db.GetDB().Model(&model.RelayLog{}).Pluck("id", &remaining).Error; err != nil {
		test.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0] != 2 {
		test.Fatalf("remaining log IDs = %v, want [2]", remaining)
	}
}

func TestRelayLogCapacityDeletesOldestAndReclaimsSpace(test *testing.T) {
	ctx := setupLogStorageTest(test, "2")
	if err := SettingSetString(model.SettingKeyRelayLogKeepPeriod, "0"); err != nil {
		test.Fatal(err)
	}
	account := model.User{Username: "preserved", Password: "test-only"}
	if err := db.GetDB().Create(&account).Error; err != nil {
		test.Fatal(err)
	}
	rows := make([]model.RelayLog, 500)
	for index := range rows {
		rows[index] = model.RelayLog{ID: int64(index + 1), Time: time.Now().Unix() + int64(index), RequestContent: strings.Repeat("x", 4096)}
	}
	if err := db.GetDB().CreateInBatches(&rows, 100).Error; err != nil {
		test.Fatal(err)
	}
	before, err := db.ReadSQLiteStorage(ctx)
	if err != nil {
		test.Fatal(err)
	}
	if before.AutoVacuum != 2 || before.UsedBytes() <= 2*1024*1024 {
		test.Fatalf("unexpected storage before cleanup: %+v", before)
	}
	if err := RelayLogMaintainStorage(ctx); err != nil {
		test.Fatal(err)
	}
	after, err := db.ReadSQLiteStorage(ctx)
	if err != nil {
		test.Fatal(err)
	}
	if after.UsedBytes() > 2*1024*1024 || after.PageCount*after.PageSize > 2*1024*1024 {
		test.Fatalf("storage remains over limit: %+v", after)
	}
	var remaining []int64
	if err := db.GetDB().Model(&model.RelayLog{}).Order("id").Pluck("id", &remaining).Error; err != nil {
		test.Fatal(err)
	}
	if len(remaining) != 250 || remaining[len(remaining)-1] != 500 || remaining[0] != 251 {
		test.Fatalf("oldest-first cleanup failed: %v", remaining)
	}
	var accountCount int64
	if err := db.GetDB().Model(&model.User{}).Where("id = ?", account.ID).Count(&accountCount).Error; err != nil || accountCount != 1 {
		test.Fatalf("non-log account was modified: count=%d, error=%v", accountCount, err)
	}
}

func TestRelayLogWriterEnforcesCapacityWithoutMaintenance(test *testing.T) {
	ctx := setupLogStorageTest(test, "1")
	for batch := 0; batch < 8; batch++ {
		for index := 0; index < 70; index++ {
			if err := RelayLogAdd(ctx, model.RelayLog{Time: time.Now().Unix(), RequestContent: strings.Repeat("x", 4096)}); err != nil {
				test.Fatal(err)
			}
		}
		if err := RelayLogFlushPending(ctx); err != nil {
			test.Fatal(err)
		}
		storage, err := db.ReadSQLiteStorage(ctx)
		if err != nil || storage.UsedBytes() > 1024*1024 {
			test.Fatalf("batch %d exceeded budget: %+v, error=%v", batch, storage, err)
		}
	}
}

func TestRelayLogOversizedEntryDoesNotBlockWriter(test *testing.T) {
	ctx := setupLogStorageTest(test, "1")
	if err := RelayLogAdd(ctx, model.RelayLog{Time: time.Now().Unix(), RequestContent: strings.Repeat("x", 1024*1024)}); err != nil {
		test.Fatal(err)
	}
	if err := RelayLogAdd(ctx, model.RelayLog{Time: time.Now().Unix(), RequestContent: "small"}); err != nil {
		test.Fatal(err)
	}
	if err := RelayLogFlushPending(ctx); err != nil {
		test.Fatal(err)
	}
	var rows []model.RelayLog
	if err := db.GetDB().Find(&rows).Error; err != nil {
		test.Fatal(err)
	}
	if RelayLogPendingLen() != 0 || RelayLogDroppedTotal() != 1 || len(rows) != 1 || rows[0].RequestContent != "small" {
		test.Fatalf("oversized entry blocked writer: pending=%d, dropped=%d, rows=%d", RelayLogPendingLen(), RelayLogDroppedTotal(), len(rows))
	}
}

func TestRelayLogStorageLimitPreservesNonLogData(test *testing.T) {
	ctx := setupLogStorageTest(test, "1")
	account := model.User{Username: "large-account", Password: strings.Repeat("x", 2*1024*1024)}
	if err := db.GetDB().Create(&account).Error; err != nil {
		test.Fatal(err)
	}
	if err := relayLogEnforceStorageLimit(ctx, 0); err == nil {
		test.Fatal("expected an error when non-log data alone exceeds the limit")
	}
	var saved model.User
	if err := db.GetDB().First(&saved, account.ID).Error; err != nil || saved.Password != account.Password {
		test.Fatalf("non-log data modified: %v", err)
	}
}
