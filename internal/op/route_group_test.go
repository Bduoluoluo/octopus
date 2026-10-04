package op

import (
	"testing"
	"time"

	"github.com/xuanli27/octopus/internal/db"
	"github.com/xuanli27/octopus/internal/model"
)

func TestRouteGroupIsolationAndDeletion(t *testing.T) {
	setupAutoGroupTestDB(t)
	ctx := t.Context()
	custom, err := RouteGroupCreate("custom", ctx)
	if err != nil {
		t.Fatal(err)
	}
	customCtx := WithRouteGroup(ctx, custom.ID)
	defaultRoute := &model.Group{Name: "shared-model", Mode: model.GroupModeRoundRobin}
	customRoute := &model.Group{Name: "shared-model", Mode: model.GroupModeFailover}
	for _, entry := range []struct {
		route *model.Group
		id    int
	}{{defaultRoute, model.DefaultRouteGroupID}, {customRoute, custom.ID}} {
		if err := GroupCreate(entry.route, WithRouteGroup(ctx, entry.id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := groupRefreshCache(ctx); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []*model.Group{defaultRoute, customRoute} {
		found, err := GroupGetEnabledMap("shared-model", WithRouteGroup(ctx, entry.RouteGroupID))
		if err != nil || found.ID != entry.ID {
			t.Fatalf("route group lookup crossed boundaries: %+v %v", found, err)
		}
	}
	if err := GroupCreate(&model.Group{Name: "private-model", Mode: model.GroupModeRoundRobin}, customCtx); err != nil {
		t.Fatal(err)
	}
	if _, err := GroupGetEnabledMap("private-model", ctx); err == nil {
		t.Fatal("default must not resolve a custom group's model")
	}
	models, err := GroupListModel(ctx)
	if err != nil || len(models) != 1 || models[0] != "shared-model" {
		t.Fatalf("model list leaked another group: %v %v", models, err)
	}
	newName := "renamed"
	if _, err := GroupUpdate(&model.GroupUpdateRequest{ID: customRoute.ID, Name: &newName}, ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := GroupGetEnabledMap("shared-model", customCtx); err == nil {
		t.Fatal("old route name remains cached")
	}
	if _, err := GroupGetEnabledMap("shared-model", ctx); err != nil {
		t.Fatal("renaming custom route affected default", err)
	}
	for _, route := range []*model.Group{defaultRoute, customRoute} {
		if err := GroupItemAdd(&model.GroupItem{GroupID: route.ID, ChannelID: 42, ModelName: "upstream-model"}, ctx); err != nil {
			t.Fatal(err)
		}
		preset := model.GroupPreset{GroupID: route.ID, Name: "preset", Mode: model.GroupModeRoundRobin}
		if err := db.GetDB().Create(&preset).Error; err != nil {
			t.Fatal(err)
		}
		snapshot := model.GroupHealthSnapshot{GroupID: route.ID, GroupName: route.Name, GroupMode: route.Mode, Status: model.GroupHealthStatusSuccess, StartedAt: time.Now(), Attempts: []model.GroupHealthAttempt{{Status: model.GroupHealthAttemptStatusSuccess}}}
		if err := db.GetDB().Create(&snapshot).Error; err != nil {
			t.Fatal(err)
		}
	}
	key := &model.APIKey{Name: "custom-key", APIKey: "test-route-group-key", RouteGroupID: custom.ID, Enabled: true}
	if err := APIKeyCreate(key, ctx); err != nil {
		t.Fatal(err)
	}
	if err := RouteGroupDelete(custom.ID, ctx); err == nil {
		t.Fatal("bound group deletion must be rejected")
	}
	key.RouteGroupID = 0
	if err := APIKeyUpdate(key, ctx); err != nil || key.RouteGroupID != custom.ID {
		t.Fatalf("legacy key update changed routing: %+v %v", key, err)
	}
	key.RouteGroupID = model.DefaultRouteGroupID
	if err := APIKeyUpdate(key, ctx); err != nil {
		t.Fatal(err)
	}
	if err := RouteGroupDelete(model.DefaultRouteGroupID, ctx); err == nil {
		t.Fatal("default deletion must be rejected")
	}
	if err := RouteGroupDelete(custom.ID, ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := GroupGetEnabledMap(newName, customCtx); err == nil {
		t.Fatal("deleted group remains cached")
	}
	var count int64
	if err := db.GetDB().Model(&model.Group{}).Where("route_group_id = ?", custom.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("deleted group's routes remain: %d %v", count, err)
	}
	for _, table := range []any{&model.Group{}, &model.GroupItem{}, &model.GroupPreset{}, &model.GroupHealthSnapshot{}, &model.GroupHealthAttempt{}} {
		if err := db.GetDB().Model(table).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("expected only default group data in %T: count=%d err=%v", table, count, err)
		}
	}
}

func TestRouteGroupAutoGroupStaysInSelectedGroup(t *testing.T) {
	setupAutoGroupTestDB(t)
	ctx := t.Context()
	custom, err := RouteGroupCreate("custom", ctx)
	if err != nil {
		t.Fatal(err)
	}
	defaultRoute := &model.Group{Name: "shared-model", Mode: model.GroupModeRoundRobin}
	customRoute := &model.Group{Name: "shared-model", RouteGroupID: custom.ID, Mode: model.GroupModeRoundRobin}
	for _, route := range []*model.Group{defaultRoute, customRoute} {
		if err := GroupCreate(route, ctx); err != nil {
			t.Fatal(err)
		}
	}
	channel := testChannel("channel", "shared-model,new-model", model.AutoGroupTypeExact)
	if err := ChannelCreate(channel, ctx); err != nil {
		t.Fatal(err)
	}
	if err := SettingSetString(model.SettingKeyAutoGroupCreateMissingEnabled, "true"); err != nil {
		t.Fatal(err)
	}
	customCtx := WithRouteGroup(ctx, custom.ID)
	enabled := true
	mode := model.AutoGroupTypeExact
	if _, err := GroupAutoGroupConfigUpdate(&model.GroupAutoGroupConfigUpdateRequest{
		CreateMissingGroups: &enabled,
		Items:               []model.GroupAutoGroupSourceUpdateRequest{{ChannelID: channel.ID, AutoGroup: &mode}},
	}, customCtx); err != nil {
		t.Fatal(err)
	}
	if err := RunGroupAutoGroup([]int{channel.ID}, customCtx); err != nil {
		t.Fatal(err)
	}
	if len(groupModelNames(t, defaultRoute.ID, channel.ID)) != 0 {
		t.Fatal("auto grouping modified default routes")
	}
	if !hasAllModels(groupModelNames(t, customRoute.ID, channel.ID), "shared-model") {
		t.Fatal("auto grouping missed custom route")
	}
	if _, err := GroupGetEnabledMap("new-model", customCtx); err != nil {
		t.Fatal("missing model not created in selected group", err)
	}
	if _, err := GroupGetEnabledMap("new-model", ctx); err == nil {
		t.Fatal("missing model created in wrong group")
	}
	ensurePublicGroupsForChannel(channel, ctx)
	if _, err := GroupGetEnabledMap("new-model", ctx); err != nil {
		t.Fatal("default create-missing suppressed by custom group's model", err)
	}
}

func TestRouteGroupImportRemapsAndKeepsLegacyDefault(t *testing.T) {
	setupAutoGroupTestDB(t)
	ctx := t.Context()
	if _, err := RouteGroupCreate("existing", ctx); err != nil {
		t.Fatal(err)
	}
	dump := &model.DBDump{
		Version:     2,
		RouteGroups: []model.RouteGroup{{ID: 1, Name: "default"}, {ID: 2, Name: "imported"}},
		Groups: []model.Group{
			{ID: 10, Name: "shared-model", Mode: model.GroupModeRoundRobin},
			{ID: 11, Name: "shared-model", RouteGroupID: 2, Mode: model.GroupModeFailover},
		},
		APIKeys: []model.APIKey{{ID: 5, Name: "imported-key", APIKey: "imported-key-value", RouteGroupID: 2}},
	}
	if _, err := DBImportIncremental(ctx, dump); err != nil {
		t.Fatal(err)
	}
	if _, err := DBImportIncremental(ctx, dump); err != nil {
		t.Fatal("reimport failed", err)
	}
	var imported model.RouteGroup
	if err := db.GetDB().Where("name = ?", "imported").First(&imported).Error; err != nil {
		t.Fatal(err)
	}
	if imported.ID == 2 {
		t.Fatal("expected ID remapping")
	}
	exported, err := DBExportAll(ctx, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(exported.Groups) != 2 || len(exported.RouteGroups) != 3 || exported.APIKeys[0].RouteGroupID != imported.ID {
		t.Fatalf("import/export lost route group boundaries: %+v", exported)
	}
	legacy := &model.DBDump{Version: 1, Groups: []model.Group{{ID: 15, Name: "legacy-model", Mode: model.GroupModeRoundRobin}}, APIKeys: []model.APIKey{{Name: "legacy-key", APIKey: "legacy-key-value"}}}
	if _, err := DBImportIncremental(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	var route model.Group
	if err := db.GetDB().Where("name = ?", "legacy-model").First(&route).Error; err != nil || route.RouteGroupID != model.DefaultRouteGroupID {
		t.Fatalf("legacy route not in default: %+v %v", route, err)
	}
	var key model.APIKey
	if err := db.GetDB().Where("api_key = ?", "legacy-key-value").First(&key).Error; err != nil || key.RouteGroupID != model.DefaultRouteGroupID {
		t.Fatalf("legacy key not in default: %+v %v", key, err)
	}
}
