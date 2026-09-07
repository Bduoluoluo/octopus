package db

import (
	"context"
	"fmt"
)

type SQLiteStorage struct {
	PageSize   int64
	PageCount  int64
	FreePages  int64
	AutoVacuum int
}

func (storage SQLiteStorage) UsedBytes() int64 {
	return (storage.PageCount - storage.FreePages) * storage.PageSize
}

func ReadSQLiteStorage(ctx context.Context) (SQLiteStorage, error) {
	var storage SQLiteStorage
	err := GetDB().WithContext(ctx).Raw(`SELECT page_size, page_count, freelist_count AS free_pages, auto_vacuum
		FROM pragma_page_size(), pragma_page_count(), pragma_freelist_count(), pragma_auto_vacuum()`).Scan(&storage).Error
	return storage, err
}

func ReclaimSQLiteStorage(ctx context.Context, limitBytes int64) error {
	storage, err := ReadSQLiteStorage(ctx)
	if err != nil {
		return err
	}
	for storage.AutoVacuum == 2 && storage.FreePages > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		pages := storage.FreePages
		if pages > 4096 {
			pages = 4096
		}
		rows, err := GetDB().WithContext(ctx).Raw(fmt.Sprintf("PRAGMA incremental_vacuum(%d)", pages)).Rows()
		if err != nil {
			return err
		}
		for rows.Next() {
		}
		rowErr := rows.Err()
		closeErr := rows.Close()
		if rowErr != nil {
			return rowErr
		}
		if closeErr != nil {
			return closeErr
		}
		previous := storage.FreePages
		storage, err = ReadSQLiteStorage(ctx)
		if err != nil {
			return err
		}
		if storage.FreePages >= previous {
			return fmt.Errorf("SQLite incremental vacuum did not reclaim free pages")
		}
	}
	var checkpoint struct {
		Busy         int
		Log          int
		Checkpointed int
	}
	if err := GetDB().WithContext(ctx).Raw("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&checkpoint).Error; err != nil {
		return err
	}
	if checkpoint.Busy != 0 {
		return fmt.Errorf("SQLite WAL checkpoint is busy; a reader or writer is preventing disk space reclamation")
	}
	if storage.AutoVacuum == 0 && storage.PageCount*storage.PageSize > limitBytes {
		return fmt.Errorf("SQLite file exceeds the storage limit with auto_vacuum=NONE; automatic disk space reclamation requires a database created with incremental vacuum")
	}
	return nil
}

func SetSQLiteStorageLimit(ctx context.Context, limitBytes int64) error {
	storage, err := ReadSQLiteStorage(ctx)
	if err != nil {
		return err
	}
	if storage.PageSize <= 0 || limitBytes < storage.PageSize {
		return fmt.Errorf("invalid SQLite storage limit: %d", limitBytes)
	}
	pages := limitBytes / storage.PageSize
	var effective int64
	if err := GetDB().WithContext(ctx).Raw(fmt.Sprintf("PRAGMA max_page_count = %d", pages)).Scan(&effective).Error; err != nil {
		return err
	}
	if effective > pages {
		return fmt.Errorf("SQLite database is larger than the requested storage limit; log cleanup must free more space")
	}
	return nil
}
