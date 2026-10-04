package migrate

import (
	"errors"
	"fmt"

	"github.com/xuanli27/octopus/internal/model"
	"gorm.io/gorm"
)

func init() {
	RegisterBeforeAutoMigration(Migration{Version: 21, Up: migrateRouteGroups})
}

func migrateRouteGroups(conn *gorm.DB) error {
	if err := conn.AutoMigrate(&model.RouteGroup{}); err != nil {
		return err
	}
	defaultGroup := model.RouteGroup{Name: "default"}
	if err := conn.Where("name = ?", "default").FirstOrCreate(&defaultGroup).Error; err != nil {
		return err
	}
	if defaultGroup.ID != model.DefaultRouteGroupID {
		return fmt.Errorf("default route group has unexpected id: %d", defaultGroup.ID)
	}
	for _, table := range []any{&model.Group{}, &model.APIKey{}} {
		if !conn.Migrator().HasTable(table) {
			continue
		}
		if !conn.Migrator().HasColumn(table, "RouteGroupID") {
			if err := conn.Migrator().AddColumn(table, "RouteGroupID"); err != nil {
				return err
			}
		}
		if err := conn.Model(table).Where("route_group_id IS NULL OR route_group_id = 0").Update("route_group_id", model.DefaultRouteGroupID).Error; err != nil {
			return err
		}
	}
	if conn.Migrator().HasTable(&model.Group{}) {
		for _, name := range []string{"uni_groups_name", "groups_name_key"} {
			if conn.Migrator().HasConstraint(&model.Group{}, name) {
				if err := dropLegacyGroupNameConstraint(conn, name); err != nil {
					return err
				}
			}
		}
		for _, name := range []string{"uni_groups_name", "idx_groups_name", "name"} {
			if conn.Migrator().HasIndex(&model.Group{}, name) {
				if err := conn.Migrator().DropIndex(&model.Group{}, name); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func dropLegacyGroupNameConstraint(conn *gorm.DB, name string) error {
	if conn.Dialector.Name() != "sqlite" {
		return conn.Migrator().DropConstraint(&model.Group{}, name)
	}
	return conn.Connection(func(pinned *gorm.DB) (err error) {
		var foreignKeys int
		if err = pinned.Raw("PRAGMA foreign_keys").Scan(&foreignKeys).Error; err != nil {
			return err
		}
		if foreignKeys != 0 {
			if err = pinned.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
				return err
			}
			defer func() {
				err = errors.Join(err, pinned.Exec("PRAGMA foreign_keys = ON").Error)
			}()
		}
		return pinned.Migrator().DropConstraint(&model.Group{}, name)
	})
}
