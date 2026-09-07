package op

import (
	"context"
	"fmt"
	"time"

	"github.com/xuanli27/octopus/internal/db"
	"github.com/xuanli27/octopus/internal/model"
	"github.com/xuanli27/octopus/internal/utils/log"
)

func relayLogStorageLimit() (int64, error) {
	limitMB, err := SettingGetInt(model.SettingKeyRelayLogMaxDBSizeMB)
	if err != nil {
		return 0, err
	}
	if limitMB < 1 || limitMB > 1048576 {
		return 0, fmt.Errorf("invalid relay log database size limit: %d MiB", limitMB)
	}
	return int64(limitMB) * 1024 * 1024, nil
}

func relayLogEnforceStorageLimit(ctx context.Context, reserveBytes int64) error {
	if db.GetDB().Dialector.Name() != "sqlite" {
		return nil
	}
	limitBytes, err := relayLogStorageLimit()
	if err != nil {
		return err
	}
	storage, err := db.ReadSQLiteStorage(ctx)
	if err != nil {
		return err
	}
	if storage.UsedBytes()+reserveBytes <= limitBytes {
		if storage.PageCount*storage.PageSize > limitBytes {
			if err := db.ReclaimSQLiteStorage(ctx, limitBytes); err != nil {
				return err
			}
		}
		return db.SetSQLiteStorageLimit(ctx, limitBytes)
	}
	deletedRows := int64(0)
	defer func() {
		if deletedRows > 0 {
			log.Infow("relay_log.capacity_cleanup", "deleted_rows", deletedRows, "limit_bytes", limitBytes)
		}
	}()
	connection := db.GetDB().WithContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	var totalRows int64
	if err := connection.Model(&model.RelayLog{}).Count(&totalRows).Error; err != nil {
		return err
	}
	if totalRows == 0 {
		return fmt.Errorf("SQLite non-log data exceeds the available log budget; log persistence is paused, other data is not deleted")
	}
	deleteCount := (totalRows + 1) / 2
	result := connection.Exec(`DELETE FROM relay_logs WHERE id IN
		(SELECT id FROM relay_logs ORDER BY time ASC, id ASC LIMIT ?)`, deleteCount)
	if result.Error != nil {
		return result.Error
	}
	deletedRows = result.RowsAffected
	storage, err = db.ReadSQLiteStorage(ctx)
	if err != nil {
		return err
	}
	if err := db.ReclaimSQLiteStorage(ctx, limitBytes); err != nil {
		return err
	}
	if err := db.SetSQLiteStorageLimit(ctx, limitBytes); err != nil {
		return err
	}
	if storage.UsedBytes()+reserveBytes > limitBytes {
		return fmt.Errorf("SQLite storage budget is still insufficient after halving logs; new log writes are paused until further cleanup")
	}
	return nil
}

func RelayLogMaintainStorage(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return relayLogMaintainStorage(ctx)
}
