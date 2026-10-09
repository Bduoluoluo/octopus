package op

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/xuanli27/octopus/internal/db"
	"github.com/xuanli27/octopus/internal/model"
	"github.com/xuanli27/octopus/internal/transformer/outbound"
)

func TestRetiredChannelManagement(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	if err := channelRefreshCache(ctx); err != nil {
		t.Fatal(err)
	}
	for _, channelType := range []outbound.OutboundType{3, 4, 5, 99} {
		channel := &model.Channel{Name: fmt.Sprintf("legacy-%d", channelType), Type: channelType, Enabled: true}
		if err := ChannelCreate(channel, ctx); !errors.Is(err, model.ErrUnsupportedChannelType) {
			t.Fatalf("create %d: %v", channelType, err)
		}
		if err := db.GetDB().WithContext(ctx).Create(channel).Error; err != nil {
			t.Fatal(err)
		}
		if err := channelRefreshCacheByID(channel.ID, ctx); err != nil {
			t.Fatal(err)
		}
		enabled := true
		if _, err := ChannelUpdate(&model.ChannelUpdateRequest{ID: channel.ID, Enabled: &enabled}, ctx); !errors.Is(err, model.ErrUnsupportedChannelType) {
			t.Fatalf("update enable %d: %v", channelType, err)
		}
		for _, enable := range []func(int, bool, context.Context) error{ChannelEnabled, ChannelEnabledManaged} {
			if err := enable(channel.ID, true, ctx); !errors.Is(err, model.ErrUnsupportedChannelType) {
				t.Fatalf("enable %d: %v", channelType, err)
			}
			if err := enable(channel.ID, false, ctx); err != nil {
				t.Fatal(err)
			}
		}
		name := channel.Name + "-renamed"
		if _, err := ChannelUpdate(&model.ChannelUpdateRequest{ID: channel.ID, Name: &name}, ctx); err != nil {
			t.Fatal(err)
		}
		reloaded, err := ChannelGet(channel.ID, ctx)
		if err != nil || reloaded.Type != channelType || reloaded.Enabled {
			t.Fatalf("history changed: %+v %v", reloaded, err)
		}
		if err := ChannelDel(channel.ID, ctx); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBackupRetiredProtocolsRemainReadable(t *testing.T) {
	ctx := setupBackupTestDB(t)
	dump := &model.DBDump{Version: dbDumpVersion}
	for _, channelType := range []outbound.OutboundType{3, 4, 5} {
		dump.Channels = append(dump.Channels, model.Channel{
			ID: int(channelType), Name: fmt.Sprintf("backup-%d", channelType), Type: channelType, Enabled: channelType != 4,
		})
	}
	dump.Groups = []model.Group{{ID: 10, Name: "legacy-group", Mode: model.GroupModeFailover}}
	dump.GroupItems = []model.GroupItem{{ID: 20, GroupID: 10, ChannelID: 3, ModelName: "gemini-history", Weight: 2, Priority: 7}}
	if _, err := DBImportIncremental(ctx, dump); err != nil {
		t.Fatal(err)
	}
	exported, err := DBExportAll(ctx, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(exported.Channels) != 3 || len(exported.GroupItems) != 1 {
		t.Fatalf("historical config dropped: channels=%d items=%d", len(exported.Channels), len(exported.GroupItems))
	}
	for _, channel := range exported.Channels {
		if !outbound.IsRetired(channel.Type) || channel.Enabled != (channel.Type != 4) {
			t.Fatalf("backup rewrote type or enabled: %+v", channel)
		}
	}
	dump.Channels[0].Type = 99
	if _, err := DBImportIncremental(ctx, dump); !errors.Is(err, model.ErrUnsupportedChannelType) {
		t.Fatalf("unknown backup type accepted: %v", err)
	}
}

func TestRetiredSiteConfigurationCanBeDisabled(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	site := model.Site{
		Name: "legacy site", Platform: model.SitePlatformAPI, BaseURL: "https://example.com", Enabled: true,
		DefaultRouteType: model.SiteModelRouteTypeGemini,
		RouteBaseURLs:    []model.SiteRouteBaseURL{{RouteType: model.SiteModelRouteTypeGemini, BaseURL: "https://example.com/v1beta"}},
	}
	if err := SiteCreate(&site, ctx); err == nil {
		t.Fatal("created site using retired protocol")
	}
	if err := db.GetDB().Create(&site).Error; err != nil {
		t.Fatal(err)
	}
	disabled := false
	updated, err := SiteUpdate(&model.SiteUpdateRequest{ID: site.ID, Enabled: &disabled}, ctx)
	if err != nil || updated.Enabled || updated.DefaultRouteType != site.DefaultRouteType || len(updated.RouteBaseURLs) != 1 {
		t.Fatalf("cannot disable without rewriting history: %+v %v", updated, err)
	}
	enabled := true
	if _, err := SiteUpdate(&model.SiteUpdateRequest{ID: site.ID, Enabled: &enabled}, ctx); err == nil {
		t.Fatal("enabled retired site default")
	}
	if err := SiteModelRouteUpdate(1, "default", "vendor", model.SiteModelRouteTypeGemini, model.SiteModelRouteSourceManualOverride, true, "", ctx); err == nil {
		t.Fatal("accepted retired manual route")
	}
}
