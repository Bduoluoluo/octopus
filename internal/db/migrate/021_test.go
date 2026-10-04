package migrate

import (
	"testing"

	"github.com/xuanli27/octopus/internal/model"
)

func TestMigrateRouteGroupsPreservesLegacyRoutes(t *testing.T) {
	conn := openMigrationTestDB(t)
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := conn.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Exec("CREATE TABLE groups (id integer PRIMARY KEY AUTOINCREMENT, name text NOT NULL, mode integer NOT NULL, CONSTRAINT uni_groups_name UNIQUE (name))").Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Exec("INSERT INTO groups (id, name, mode) VALUES (7, 'same-model', 1)").Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.AutoMigrate(&model.GroupItem{}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Migrator().CreateConstraint(&model.Group{}, "Items"); err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&model.GroupItem{GroupID: 7, ChannelID: 3, ModelName: "upstream-model"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Exec("CREATE TABLE api_keys (id integer PRIMARY KEY AUTOINCREMENT, name text NOT NULL, api_key text NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Exec("INSERT INTO api_keys (id, name, api_key) VALUES (9, 'legacy', 'legacy-key')").Error; err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := migrateRouteGroups(conn); err != nil {
			t.Fatal(err)
		}
	}
	var foreignKeys int
	if err := conn.Raw("PRAGMA foreign_keys").Scan(&foreignKeys).Error; err != nil || foreignKeys != 1 {
		t.Fatalf("foreign key enforcement was not restored: %d %v", foreignKeys, err)
	}
	if err := conn.AutoMigrate(&model.Group{}, &model.APIKey{}); err != nil {
		t.Fatal(err)
	}
	var legacy model.Group
	if err := conn.First(&legacy, 7).Error; err != nil {
		t.Fatal(err)
	}
	if legacy.Name != "same-model" || legacy.RouteGroupID != model.DefaultRouteGroupID {
		t.Fatalf("legacy route changed: %+v", legacy)
	}
	var item model.GroupItem
	if err := conn.Where("group_id = ?", legacy.ID).First(&item).Error; err != nil || item.ModelName != "upstream-model" {
		t.Fatalf("legacy channel mapping changed: %+v %v", item, err)
	}
	var key model.APIKey
	if err := conn.First(&key, 9).Error; err != nil || key.RouteGroupID != model.DefaultRouteGroupID {
		t.Fatalf("legacy key not assigned to default: %+v err=%v", key, err)
	}
	custom := model.RouteGroup{Name: "custom"}
	if err := conn.Create(&custom).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&model.Group{Name: "same-model", RouteGroupID: custom.ID, Mode: model.GroupModeRoundRobin}).Error; err != nil {
		t.Fatalf("same name in another route group must be allowed: %v", err)
	}
	if err := conn.Create(&model.Group{Name: "same-model", RouteGroupID: custom.ID, Mode: model.GroupModeRoundRobin}).Error; err == nil {
		t.Fatal("duplicate name in the same route group must be rejected")
	}
}
