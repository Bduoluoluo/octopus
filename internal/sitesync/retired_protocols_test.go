package sitesync

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xuanli27/octopus/internal/db"
	"github.com/xuanli27/octopus/internal/model"
	"github.com/xuanli27/octopus/internal/op"
	"github.com/xuanli27/octopus/internal/transformer/outbound"
)

func TestProjectAccountPreservesRetiredChannelsAndGroups(t *testing.T) {
	ctx := setupProjectTestDB(t)
	site, account := createProjectionFixture(t, ctx)
	dump := &model.DBDump{Version: 1}
	for _, channelType := range []outbound.OutboundType{3, 4, 5} {
		channelID := int(channelType) + 100
		dump.Channels = append(dump.Channels, model.Channel{ID: channelID, Name: fmt.Sprintf("legacy-%d", channelType), Type: channelType, Enabled: true})
		dump.GroupItems = append(dump.GroupItems, model.GroupItem{ID: channelID, GroupID: 100, ChannelID: channelID, ModelName: "legacy-model", Weight: 3, Priority: 7})
	}
	dump.Groups = []model.Group{{ID: 100, Name: "legacy-model", Mode: model.GroupModeFailover}}
	if _, err := op.DBImportIncremental(ctx, dump); err != nil {
		t.Fatal(err)
	}
	if err := op.InitCache(); err != nil {
		t.Fatal(err)
	}
	channels, err := op.ChannelList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 3 {
		t.Fatalf("expected three restored historical channels: %d", len(channels))
	}
	for _, channel := range channels {
		if !outbound.IsRetired(channel.Type) {
			continue
		}
		binding := model.SiteChannelBinding{
			SiteID: site.ID, SiteAccountID: account.ID, ChannelID: channel.ID,
			GroupKey: model.ComposeSiteChannelBindingKey("default", model.SiteModelRouteTypeFromOutboundType(channel.Type), true),
		}
		if err := db.GetDB().Create(&binding).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ProjectAccount(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	for _, before := range channels {
		if !outbound.IsRetired(before.Type) {
			continue
		}
		after, err := op.ChannelGet(before.ID, ctx)
		if err != nil || after.Type != before.Type || after.Enabled != before.Enabled {
			t.Fatalf("historical channel changed: %v, %+v", err, after)
		}
	}
	var count int64
	if err := db.GetDB().Model(&model.GroupItem{}).Where("model_name = ?", "legacy-model").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("retired group membership changed: %d", count)
	}
	if err := db.GetDB().Model(&model.SiteChannelBinding{}).Where("site_account_id = ?", account.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Fatalf("expected historical bindings plus two active bindings: %d", count)
	}
}

func TestSiteProjectionPreservesVendorModelNames(t *testing.T) {
	for _, split := range []bool{false, true} {
		items := []model.SiteModel{
			{ModelName: "gemini-compatible", RouteType: model.SiteModelRouteTypeOpenAIChat},
			{ModelName: "doubao-compatible", RouteType: model.SiteModelRouteTypeOpenAIChat},
			{ModelName: "deepseek-compatible", RouteType: model.SiteModelRouteTypeOpenAIChat},
			{ModelName: "retired", RouteType: model.SiteModelRouteTypeGemini},
		}
		buckets := partitionSiteModelsByRouteType(items, split, &model.Site{Platform: model.SitePlatformAPI})
		if len(buckets) != 1 || len(buckets[model.SiteModelRouteTypeOpenAIChat]) != 3 {
			t.Fatalf("vendor names filtered or retired route projected: %+v", buckets)
		}
		if items[3].ModelName != "retired" || len(items) != 4 {
			t.Fatal("projection mutated model history")
		}
	}
}

func TestRetiredEndpointsDoNotBecomeRuntimeRoutes(t *testing.T) {
	for _, endpoint := range []string{"gemini", "/v1/embeddings", "volcengine"} {
		if _, supported := mapSupportedEndpointType(endpoint); supported {
			t.Fatalf("retired endpoint supported: %s", endpoint)
		}
		detection, ok := buildSiteModelRouteDetection("vendor-model", nil, []string{endpoint}, "test", nil)
		if !ok || detection.RouteType != model.SiteModelRouteTypeUnknown {
			t.Fatalf("retired endpoint guessed as active: %+v", detection)
		}
		metadata, ok := model.ParseSiteModelRouteMetadata(detection.RouteRawPayload)
		if !ok || metadata.RouteSupported || !strings.Contains(detection.RouteRawPayload, endpoint) {
			t.Fatalf("retired metadata lost: %+v", metadata)
		}
		heuristic, ok := buildSiteModelRouteDetection("gpt-5-vendor", nil, []string{endpoint}, "test", nil)
		if !ok || heuristic.RouteType != model.SiteModelRouteTypeUnknown {
			t.Fatalf("heuristic activated retired-only endpoint: %+v", heuristic)
		}
	}
	existing := &model.SiteModel{RouteType: model.SiteModelRouteTypeGemini, RouteSource: model.SiteModelRouteSourceSyncInferred}
	item := &model.SiteModel{ModelName: "gemini-model", RouteType: model.SiteModelRouteTypeOpenAIChat}
	applyPersistedRouteState(item, existing, time.Now())
	if item.RouteType != existing.RouteType {
		t.Fatal("sync rewrote historical retired route")
	}
}

func TestSiteAutoDetectionUsesCompatibleChat(t *testing.T) {
	platform, route, err := DetectPlatform(t.Context(), "https://generativelanguage.googleapis.com/v1beta/openai")
	if err != nil || platform != model.SitePlatformAPI || route != model.SiteModelRouteTypeOpenAIChat {
		t.Fatalf("compatible endpoint was retired by vendor name: %s %s %v", platform, route, err)
	}
}
