package op

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/xuanli27/octopus/internal/db"
	"github.com/xuanli27/octopus/internal/model"
	outboundmodel "github.com/xuanli27/octopus/internal/transformer/outbound"
)

func GroupAutoGroupConfigGet(ctx context.Context) (*model.GroupAutoGroupConfig, error) {
	config, err := loadRouteAutoGroupConfig(ctx)
	if err != nil {
		return nil, err
	}
	channels, err := ChannelList(ctx)
	if err != nil {
		return nil, err
	}

	channelIDs := make([]int, 0, len(channels))
	for _, channel := range channels {
		channelIDs = append(channelIDs, channel.ID)
	}
	bindingMap, err := SiteChannelBindingMapByChannelIDs(channelIDs, ctx)
	if err != nil {
		return nil, err
	}

	globalMode := config.projectedMode
	sources := make([]model.GroupAutoGroupSource, 0, len(channels))
	for _, channel := range channels {
		models := splitChannelModelNames(channel.Model, channel.CustomModel)
		binding, managed := bindingMap[channel.ID]
		effective := config.channelMode(channel)
		globalOverride := managed && globalMode != model.AutoGroupTypeNone
		if globalOverride {
			effective = globalMode
		}

		source := model.GroupAutoGroupSource{
			ChannelID:          channel.ID,
			ChannelName:        channel.Name,
			Enabled:            channel.Enabled,
			Managed:            managed,
			AutoGroup:          config.channelMode(channel),
			EffectiveAutoGroup: effective,
			GlobalOverride:     globalOverride,
			ModelCount:         len(models),
			Models:             models,
			EndpointType:       channelEndpointType(channel.Type),
		}
		if managed {
			fillManagedAutoGroupSourceMetadata(&source, binding, ctx)
		}
		sources = append(sources, source)
	}

	sort.Slice(sources, func(i, j int) bool {
		left, right := autoGroupSourceSortName(sources[i]), autoGroupSourceSortName(sources[j])
		if left == right {
			return sources[i].ChannelID < sources[j].ChannelID
		}
		return left < right
	})

	return &model.GroupAutoGroupConfig{
		ProjectedGlobalAutoGroup: globalMode,
		CreateMissingGroups:      config.createMissing,
		NormalizeModelNames:      config.normalize,
		Sources:                  sources,
	}, nil
}

func GroupAutoGroupConfigUpdate(req *model.GroupAutoGroupConfigUpdateRequest, ctx context.Context) (*model.GroupAutoGroupConfig, error) {
	if req == nil {
		return nil, newGroupAutoGroupBadRequestError("auto group config request is nil")
	}
	if err := validateRouteGroup(db.GetDB().WithContext(ctx), RouteGroupIDFromContext(ctx)); err != nil {
		return nil, newGroupAutoGroupBadRequestError(err.Error())
	}

	if req.ProjectedGlobalAutoGroup != nil {
		if !req.ProjectedGlobalAutoGroup.Valid() {
			return nil, newGroupAutoGroupBadRequestError("invalid projected global auto group type")
		}
	}
	seen := make(map[int]struct{}, len(req.Items))
	for _, item := range req.Items {
		if item.ChannelID <= 0 {
			return nil, newGroupAutoGroupBadRequestError("channel id is required")
		}
		if _, ok := seen[item.ChannelID]; ok {
			return nil, newGroupAutoGroupBadRequestError(fmt.Sprintf("duplicate channel: %d", item.ChannelID))
		}
		seen[item.ChannelID] = struct{}{}
		if _, ok := channelCache.Get(item.ChannelID); !ok {
			return nil, newGroupAutoGroupNotFoundError(fmt.Sprintf("channel not found: %d", item.ChannelID))
		}
		if item.AutoGroup == nil {
			continue
		}
		if !item.AutoGroup.Valid() {
			return nil, newGroupAutoGroupBadRequestError("invalid auto group type")
		}
	}

	if err := saveRouteAutoGroupConfig(req, ctx); err != nil {
		return nil, err
	}

	if req.RunNow {
		if err := RunGroupAutoGroup(nil, ctx); err != nil {
			return nil, err
		}
	} else if req.ProjectedGlobalAutoGroup != nil && *req.ProjectedGlobalAutoGroup != model.AutoGroupTypeNone {
		if err := AutoGroupAllProjectedChannels(ctx); err != nil {
			return nil, err
		}
	}

	return GroupAutoGroupConfigGet(ctx)
}

func RunGroupAutoGroup(channelIDs []int, ctx context.Context) error {
	config, err := loadRouteAutoGroupConfig(ctx)
	if err != nil {
		return newGroupAutoGroupBadRequestError(err.Error())
	}
	allChannels := channelCache.GetAll()
	if len(allChannels) == 0 {
		return nil
	}

	targets := make(map[int]model.Channel)
	if len(channelIDs) == 0 {
		for id, channel := range allChannels {
			targets[id] = channel
		}
	} else {
		for _, id := range channelIDs {
			if _, ok := targets[id]; ok {
				continue
			}
			channel, ok := allChannels[id]
			if !ok {
				return newGroupAutoGroupNotFoundError(fmt.Sprintf("channel not found: %d", id))
			}
			targets[id] = channel
		}
	}

	targetIDs := make([]int, 0, len(targets))
	for id := range targets {
		targetIDs = append(targetIDs, id)
	}
	bindingMap, err := SiteChannelBindingMapByChannelIDs(targetIDs, ctx)
	if err != nil {
		return err
	}
	for id, channel := range targets {
		_, managed := bindingMap[id]
		mode := config.effectiveMode(channel, managed)
		if mode == model.AutoGroupTypeNone {
			continue
		}
		channelAutoGroupWithConfig(&channel, mode, config, ctx)
	}
	return nil
}

func fillManagedAutoGroupSourceMetadata(source *model.GroupAutoGroupSource, binding model.SiteChannelBinding, ctx context.Context) {
	if source == nil {
		return
	}
	siteID := binding.SiteID
	accountID := binding.SiteAccountID
	source.SiteID = &siteID
	source.SiteAccountID = &accountID
	baseGroupKey, routeType := model.ParseSiteChannelBindingKey(binding.GroupKey)
	source.SiteGroupKey = baseGroupKey
	if routeType != "" {
		source.EndpointType = string(routeType)
	}
	source.SiteGroupName = model.NormalizeSiteGroupName(baseGroupKey, "")

	site, err := SiteGet(binding.SiteID, ctx)
	if err != nil || site == nil {
		return
	}
	source.SiteName = site.Name
	for i := range site.Accounts {
		account := &site.Accounts[i]
		if account.ID != binding.SiteAccountID {
			continue
		}
		source.SiteAccountName = account.Name
		for _, group := range account.UserGroups {
			groupKey := model.NormalizeSiteGroupKey(group.GroupKey)
			if binding.SiteUserGroupID != nil && group.ID == *binding.SiteUserGroupID {
				source.SiteGroupKey = groupKey
				source.SiteGroupName = model.NormalizeSiteGroupName(groupKey, group.Name)
				return
			}
			if groupKey == baseGroupKey {
				source.SiteGroupName = model.NormalizeSiteGroupName(groupKey, group.Name)
			}
		}
		return
	}
}

func autoGroupSourceSortName(source model.GroupAutoGroupSource) string {
	parts := []string{source.ChannelName}
	if source.Managed {
		parts = []string{source.SiteName, source.SiteAccountName, source.SiteGroupName, source.EndpointType, source.ChannelName}
	}
	return strings.ToLower(strings.Join(parts, "/"))
}

func channelEndpointType(channelType outboundmodel.OutboundType) string {
	switch channelType {
	case outboundmodel.OutboundTypeOpenAIResponse:
		return string(model.SiteModelRouteTypeOpenAIResponse)
	case outboundmodel.OutboundTypeAnthropic:
		return string(model.SiteModelRouteTypeAnthropic)
	case outboundmodel.OutboundTypeGemini:
		return string(model.SiteModelRouteTypeGemini)
	case outboundmodel.OutboundTypeVolcengine:
		return string(model.SiteModelRouteTypeVolcengine)
	case outboundmodel.OutboundTypeOpenAIEmbedding:
		return string(model.SiteModelRouteTypeOpenAIEmbedding)
	default:
		return string(model.SiteModelRouteTypeOpenAIChat)
	}
}
