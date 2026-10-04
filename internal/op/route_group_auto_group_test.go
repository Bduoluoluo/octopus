package op

import (
	"context"
	"testing"

	"github.com/xuanli27/octopus/internal/db"
	"github.com/xuanli27/octopus/internal/model"
)

func TestRouteAutoGroupConfigIsolation(t *testing.T) {
	setupAutoGroupTestDB(t)
	ctx := t.Context()
	first, err := RouteGroupCreate("first", ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RouteGroupCreate("second", ctx)
	if err != nil {
		t.Fatal(err)
	}
	channel := testChannel("shared", "gpt-4o", model.AutoGroupTypeFuzzy)
	if err := ChannelCreate(channel, ctx); err != nil {
		t.Fatal(err)
	}
	if err := SettingSetString(model.SettingKeyProjectedChannelAutoGroupEnabled, "true"); err != nil {
		t.Fatal(err)
	}
	if err := SettingSetString(model.SettingKeyAutoGroupNormalizeEnabled, "true"); err != nil {
		t.Fatal(err)
	}
	read := func(id int) *model.GroupAutoGroupConfig {
		t.Helper()
		config, err := GroupAutoGroupConfigGet(WithRouteGroup(ctx, id))
		if err != nil {
			t.Fatal(err)
		}
		return config
	}
	assertConfig := func(id int, mode, projected model.AutoGroupType, create, normalize bool) {
		t.Helper()
		config := read(id)
		if len(config.Sources) != 1 || config.Sources[0].AutoGroup != mode || config.ProjectedGlobalAutoGroup != projected || config.CreateMissingGroups != create || config.NormalizeModelNames != normalize {
			t.Fatalf("wrong config for group %d: %+v", id, config)
		}
	}
	assertConfig(model.DefaultRouteGroupID, model.AutoGroupTypeFuzzy, model.AutoGroupTypeFuzzy, false, true)
	assertConfig(first.ID, model.AutoGroupTypeNone, model.AutoGroupTypeNone, false, false)
	assertConfig(second.ID, model.AutoGroupTypeNone, model.AutoGroupTypeNone, false, false)
	enabled, disabled := true, false
	exact, regex, none := model.AutoGroupTypeExact, model.AutoGroupTypeRegex, model.AutoGroupTypeNone
	if _, err := GroupAutoGroupConfigUpdate(&model.GroupAutoGroupConfigUpdateRequest{
		ProjectedGlobalAutoGroup: &regex, CreateMissingGroups: &enabled, NormalizeModelNames: &enabled,
		Items: []model.GroupAutoGroupSourceUpdateRequest{{ChannelID: channel.ID, AutoGroup: &exact}},
	}, WithRouteGroup(ctx, first.ID)); err != nil {
		t.Fatal(err)
	}
	assertConfig(first.ID, exact, regex, true, true)
	assertConfig(second.ID, none, none, false, false)
	assertConfig(model.DefaultRouteGroupID, model.AutoGroupTypeFuzzy, model.AutoGroupTypeFuzzy, false, true)
	if _, err := GroupAutoGroupConfigUpdate(&model.GroupAutoGroupConfigUpdateRequest{
		ProjectedGlobalAutoGroup: &none, NormalizeModelNames: &disabled,
		Items: []model.GroupAutoGroupSourceUpdateRequest{{ChannelID: channel.ID, AutoGroup: &none}},
	}, ctx); err != nil {
		t.Fatal(err)
	}
	settingCache.Clear()
	channelCache.Clear()
	if err := settingRefreshCache(ctx); err != nil {
		t.Fatal(err)
	}
	if err := channelRefreshCache(ctx); err != nil {
		t.Fatal(err)
	}
	assertConfig(first.ID, exact, regex, true, true)
	assertConfig(model.DefaultRouteGroupID, none, none, false, false)
	invalid := model.AutoGroupType(99)
	if _, err := GroupAutoGroupConfigUpdate(&model.GroupAutoGroupConfigUpdateRequest{
		CreateMissingGroups: &disabled,
		Items:               []model.GroupAutoGroupSourceUpdateRequest{{ChannelID: channel.ID, AutoGroup: &invalid}},
	}, WithRouteGroup(ctx, first.ID)); err == nil {
		t.Fatal("invalid mode accepted")
	}
	assertConfig(first.ID, exact, regex, true, true)
	if _, err := GroupAutoGroupConfigUpdate(&model.GroupAutoGroupConfigUpdateRequest{
		ProjectedGlobalAutoGroup: &none, CreateMissingGroups: &disabled, NormalizeModelNames: &disabled,
		Items: []model.GroupAutoGroupSourceUpdateRequest{{ChannelID: channel.ID, AutoGroup: &none}},
	}, WithRouteGroup(ctx, first.ID)); err != nil {
		t.Fatal(err)
	}
	assertConfig(first.ID, none, none, false, false)
	if err := RouteGroupDelete(first.ID, ctx); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.GetDB().Model(&model.RouteGroupChannel{}).Where("route_group_id = ?", first.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("deleted group left settings: %d %v", count, err)
	}
}

func TestRouteAutoGroupSyncUsesIndependentRules(t *testing.T) {
	setupAutoGroupTestDB(t)
	ctx := t.Context()
	channel := testChannel("sync", "openai/gpt-4o-2024-08-06", model.AutoGroupTypeNone)
	if err := ChannelCreate(channel, ctx); err != nil {
		t.Fatal(err)
	}
	enabled, disabled := true, false
	exact := model.AutoGroupTypeExact
	groupIDs := make([]int, 0, 3)
	for _, options := range []struct {
		name      string
		normalize bool
		create    bool
	}{{"normalized", true, true}, {"raw", false, true}, {"existing-only", true, false}} {
		group, err := RouteGroupCreate(options.name, ctx)
		if err != nil {
			t.Fatal(err)
		}
		groupIDs = append(groupIDs, group.ID)
		if err := GroupCreate(&model.Group{Name: "gpt-4o", RouteGroupID: group.ID, Mode: model.GroupModeRoundRobin}, ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := GroupAutoGroupConfigUpdate(&model.GroupAutoGroupConfigUpdateRequest{
			CreateMissingGroups: &options.create, NormalizeModelNames: &options.normalize,
			Items: []model.GroupAutoGroupSourceUpdateRequest{{ChannelID: channel.ID, AutoGroup: &exact}},
		}, WithRouteGroup(ctx, group.ID)); err != nil {
			t.Fatal(err)
		}
	}
	ChannelAutoGroup(channel, ctx)
	for index, id := range groupIDs {
		route, err := GroupGetEnabledMap("gpt-4o", WithRouteGroup(ctx, id))
		if err != nil {
			t.Fatal(err)
		}
		wantItems := 1
		if index == 1 {
			wantItems = 0
		}
		if len(route.Items) != wantItems {
			t.Fatalf("normalization leaked into group %d: %+v", id, route.Items)
		}
	}
	if _, err := GroupGetEnabledMap(channel.Model, WithRouteGroup(ctx, groupIDs[1])); err != nil {
		t.Fatal("raw group did not create unnormalized route", err)
	}
	if _, err := GroupGetEnabledMap("gpt-4o", ctx); err == nil {
		t.Fatal("custom rule modified default")
	}
	channel.Model += ",new-model"
	ChannelAutoGroup(channel, ctx)
	for index, id := range groupIDs {
		_, err := GroupGetEnabledMap("new-model", WithRouteGroup(ctx, id))
		if (err == nil) != (index < 2) {
			t.Fatalf("create-missing leaked into group %d: %v", id, err)
		}
	}
	if _, err := GroupAutoGroupConfigUpdate(&model.GroupAutoGroupConfigUpdateRequest{
		CreateMissingGroups: &disabled, NormalizeModelNames: &enabled,
	}, WithRouteGroup(ctx, groupIDs[0])); err != nil {
		t.Fatal(err)
	}
	channel.Model += ",later-model"
	ChannelAutoGroup(channel, ctx)
	if _, err := GroupGetEnabledMap("later-model", WithRouteGroup(ctx, groupIDs[0])); err == nil {
		t.Fatal("disabled create-missing still applied")
	}
}

func TestRouteAutoGroupProjectedOverrideAndBackup(t *testing.T) {
	setupAutoGroupTestDB(t)
	ctx := t.Context()
	custom, err := RouteGroupCreate("projected", ctx)
	if err != nil {
		t.Fatal(err)
	}
	channel := testChannel("projected-channel", "gpt-4o", model.AutoGroupTypeNone)
	if err := ChannelCreate(channel, ctx); err != nil {
		t.Fatal(err)
	}
	site := &model.Site{Name: "site", Platform: model.SitePlatformNewAPI, BaseURL: "https://example.com", Enabled: true}
	if err := SiteCreate(site, ctx); err != nil {
		t.Fatal(err)
	}
	account := &model.SiteAccount{SiteID: site.ID, Name: "account", CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: "test", Enabled: true}
	if err := SiteAccountCreate(account, ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.GetDB().Create(&model.SiteChannelBinding{SiteID: site.ID, SiteAccountID: account.ID, GroupKey: "default", ChannelID: channel.ID}).Error; err != nil {
		t.Fatal(err)
	}
	enabled := true
	exact, regex := model.AutoGroupTypeExact, model.AutoGroupTypeRegex
	customCtx := WithRouteGroup(ctx, custom.ID)
	if _, err := GroupAutoGroupConfigUpdate(&model.GroupAutoGroupConfigUpdateRequest{
		ProjectedGlobalAutoGroup: &exact, CreateMissingGroups: &enabled,
		Items: []model.GroupAutoGroupSourceUpdateRequest{{ChannelID: channel.ID, AutoGroup: &regex}},
	}, customCtx); err != nil {
		t.Fatal(err)
	}
	if _, err := GroupGetEnabledMap("gpt-4o", customCtx); err != nil {
		t.Fatal("saved override did not run with newly saved create-missing", err)
	}
	channel.Model += ",sync-model"
	ChannelAutoGroup(channel, ctx)
	if _, err := GroupGetEnabledMap("sync-model", customCtx); err != nil {
		t.Fatal("background sync ignored custom projected override", err)
	}
	if _, err := GroupGetEnabledMap("sync-model", ctx); err == nil {
		t.Fatal("custom override changed default")
	}
	dump, err := DBExportAll(ctx, false, false)
	if err != nil {
		t.Fatal(err)
	}
	setupAutoGroupTestDB(t)
	if _, err := RouteGroupCreate("occupy-group-id", ctx); err != nil {
		t.Fatal(err)
	}
	if err := ChannelCreate(testChannel("occupy-channel-id", "unused", model.AutoGroupTypeNone), ctx); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := DBImportIncremental(ctx, dump); err != nil {
			t.Fatal(err)
		}
	}
	if err := channelRefreshCache(ctx); err != nil {
		t.Fatal(err)
	}
	var imported model.RouteGroup
	if err := db.GetDB().Where("name = ?", "projected").First(&imported).Error; err != nil {
		t.Fatal(err)
	}
	config, err := GroupAutoGroupConfigGet(WithRouteGroup(context.Background(), imported.ID))
	if err != nil {
		t.Fatal(err)
	}
	if config.ProjectedGlobalAutoGroup != exact || !config.CreateMissingGroups || imported.ID == custom.ID {
		t.Fatalf("backup lost group settings: %+v", config)
	}
	for _, source := range config.Sources {
		if source.ChannelName == channel.Name {
			if source.ChannelID == channel.ID || source.AutoGroup != regex || source.EffectiveAutoGroup != exact || !source.GlobalOverride {
				t.Fatalf("backup lost/remapped channel settings incorrectly: %+v", source)
			}
			return
		}
	}
	t.Fatal("imported channel missing")
}
