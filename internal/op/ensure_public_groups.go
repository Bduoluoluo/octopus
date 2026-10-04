package op

import (
	"context"
	"fmt"
	"strings"

	"github.com/xuanli27/octopus/internal/model"
)

// EnsurePublicGroupsResult summarizes a one-shot "generate/update public groups"
// run for a site account's projected channels.
type EnsurePublicGroupsResult struct {
	AccountID         int      `json:"account_id"`
	SiteID            int      `json:"site_id"`
	ChannelsProcessed int      `json:"channels_processed"`
	GroupsCreated     int      `json:"groups_created"`
	ItemsAdded        int      `json:"items_added"`
	CreatedGroupNames []string `json:"created_group_names,omitempty"`
	Normalize         bool     `json:"normalize"`
	Message           string   `json:"message"`
}

// EnsurePublicGroupsForSiteAccount walks projected channels of a site account
// and, using exact match (+ optional normalize), attaches models to existing
// public groups and creates missing ones. Unlike channel AutoGroup, create-
// missing is always on for this one-shot action.
func EnsurePublicGroupsForSiteAccount(siteID, accountID int, ctx context.Context) (*EnsurePublicGroupsResult, error) {
	if siteID <= 0 || accountID <= 0 {
		return nil, newSiteChannelAccountNotFoundError()
	}

	site, err := SiteGet(siteID, ctx)
	if err != nil {
		return nil, err
	}
	var account *model.SiteAccount
	for i := range site.Accounts {
		if site.Accounts[i].ID == accountID {
			account = &site.Accounts[i]
			break
		}
	}
	if account == nil {
		return nil, newSiteChannelAccountNotFoundError()
	}

	channelIDs := make([]int, 0, len(account.ChannelBindings))
	seen := make(map[int]struct{})
	for _, binding := range account.ChannelBindings {
		if binding.ChannelID <= 0 {
			continue
		}
		if _, ok := seen[binding.ChannelID]; ok {
			continue
		}
		seen[binding.ChannelID] = struct{}{}
		channelIDs = append(channelIDs, binding.ChannelID)
	}

	config, err := loadRouteAutoGroupConfig(ctx)
	if err != nil {
		return nil, err
	}
	normalize := config.normalize
	result := &EnsurePublicGroupsResult{
		AccountID: accountID,
		SiteID:    siteID,
		Normalize: normalize,
	}

	beforeGroups, err := GroupListInRouteGroup(ctx)
	if err != nil {
		return nil, err
	}
	beforeNames := make(map[string]struct{}, len(beforeGroups))
	for _, g := range beforeGroups {
		beforeNames[strings.ToLower(strings.TrimSpace(g.Name))] = struct{}{}
	}

	// Count items before for a rough items_added delta per processed channel.
	itemCountBefore := countGroupItemsForChannels(beforeGroups, seen)

	for _, channelID := range channelIDs {
		channel, err := ChannelGet(channelID, ctx)
		if err != nil || channel == nil {
			continue
		}
		if !channel.Enabled {
			continue
		}
		// One-shot: exact + create-missing always; normalize from setting.
		ensurePublicGroupsForChannel(channel, ctx)
		result.ChannelsProcessed++
	}

	afterGroups, err := GroupListInRouteGroup(ctx)
	if err != nil {
		return nil, err
	}
	created := make([]string, 0)
	for _, g := range afterGroups {
		key := strings.ToLower(strings.TrimSpace(g.Name))
		if _, ok := beforeNames[key]; !ok {
			created = append(created, g.Name)
		}
	}
	result.GroupsCreated = len(created)
	result.CreatedGroupNames = created
	itemCountAfter := countGroupItemsForChannels(afterGroups, seen)
	if itemCountAfter > itemCountBefore {
		result.ItemsAdded = itemCountAfter - itemCountBefore
	}

	parts := []string{
		fmt.Sprintf("处理 %d 个投影渠道", result.ChannelsProcessed),
		fmt.Sprintf("新建 %d 个对外分组", result.GroupsCreated),
		fmt.Sprintf("新增 %d 条挂载", result.ItemsAdded),
	}
	if normalize {
		parts = append(parts, "已启用模型名归一化")
	}
	result.Message = strings.Join(parts, "，")
	return result, nil
}

func countGroupItemsForChannels(groups []model.Group, channelIDs map[int]struct{}) int {
	total := 0
	for _, g := range groups {
		for _, item := range g.Items {
			if _, ok := channelIDs[item.ChannelID]; ok {
				total++
			}
		}
	}
	return total
}

// ensurePublicGroupsForChannel is the one-shot exact+create-missing path.
// It reuses reconcile + ensureMissingExactGroups while temporarily forcing
// create-missing semantics regardless of the global switch.
func ensurePublicGroupsForChannel(channel *model.Channel, ctx context.Context) {
	if channel == nil {
		return
	}
	config, err := loadRouteAutoGroupConfig(ctx)
	if err != nil {
		return
	}
	groups, err := GroupListInRouteGroup(ctx)
	if err != nil {
		return
	}
	channelModelNames := splitChannelModelNames(channel.Model, channel.CustomModel)
	aliasIdx, _ := loadPublicAliasIndex(ctx)
	// Exact reconcile with current normalize + alias dictionary.
	for _, group := range groups {
		desired, ok := matchModelsForAutoGroup(model.AutoGroupTypeExact, group, channelModelNames, channel.ID, config.normalize)
		if !ok {
			continue
		}
		if aliasIdx != nil {
			desired = mergeUniqueStrings(desired, modelsResolvedToPublic(channelModelNames, group.Name, aliasIdx, config.normalize))
		}
		_ = reconcileGroupItemsForChannel(group, channel.ID, desired, ctx)
	}
	// Always create missing for one-shot (create-missing global switch is only
	// checked in ChannelAutoGroupWithMode; ensureMissingExactGroups itself always creates).
	_ = ensureMissingExactGroups(channel.ID, channelModelNames, groups, config.normalize, ctx)
}
