package op

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dlclark/regexp2"
	"github.com/xuanli27/octopus/internal/model"
	"github.com/xuanli27/octopus/internal/utils/log"
)

func ProjectedChannelGlobalAutoGroupMode() model.AutoGroupType {
	value, err := SettingGetString(model.SettingKeyProjectedChannelAutoGroupEnabled)
	if err != nil {
		return model.AutoGroupTypeNone
	}
	mode, ok := model.ParseAutoGroupSettingValue(value)
	if !ok {
		return model.AutoGroupTypeNone
	}
	return mode
}

func ProjectedChannelGlobalAutoGroupEnabled() bool {
	return ProjectedChannelGlobalAutoGroupMode() != model.AutoGroupTypeNone
}

func EffectiveProjectedChannelAutoGroup(channel model.Channel) model.AutoGroupType {
	if mode := ProjectedChannelGlobalAutoGroupMode(); mode != model.AutoGroupTypeNone {
		return mode
	}
	return channel.AutoGroup
}

// ChannelAutoGroupWithMode reconciles group membership for a channel against
// the configured auto-group rule (exact / fuzzy / regex).
//
// Declarative sync (issue #105): for each group, desired = models on the
// channel that match the rule; existing items for this channel in that group
// that are not desired are removed, missing ones are added. Manual removals of
// non-matching models therefore survive the next site sync.
//
// When exact matching is used and auto_group_create_missing_enabled is true,
// models without a case-insensitive group name match also get a new Group
// created and attached (see ensureMissingExactGroups).
func ChannelAutoGroupWithMode(channel *model.Channel, autoGroup model.AutoGroupType, ctx context.Context) {
	if channel == nil || autoGroup == model.AutoGroupTypeNone {
		return
	}
	config, err := loadRouteAutoGroupConfig(ctx)
	if err != nil {
		log.Warnf("load route auto group config failed: %v", err)
		return
	}
	channelAutoGroupWithConfig(channel, autoGroup, config, ctx)
}

func channelAutoGroupWithConfig(channel *model.Channel, autoGroup model.AutoGroupType, config *routeAutoGroupConfig, ctx context.Context) {
	groups, err := GroupListInRouteGroup(ctx)
	if err != nil {
		log.Warnf("get group list failed: %v", err)
		return
	}

	channelModelNames := splitChannelModelNames(channel.Model, channel.CustomModel)

	// Alias dictionary for exact matching (public name / aliases).
	var aliasIdx *publicAliasIndex
	if autoGroup == model.AutoGroupTypeExact {
		if idx, err := loadPublicAliasIndex(ctx); err != nil {
			log.Warnf("load public model aliases failed (channel=%d): %v", channel.ID, err)
		} else {
			aliasIdx = idx
		}
	}

	for _, group := range groups {
		desired, ok := matchModelsForAutoGroup(autoGroup, group, channelModelNames, channel.ID, config.normalize)
		if !ok {
			continue
		}
		if autoGroup == model.AutoGroupTypeExact && aliasIdx != nil {
			// Also attach models whose resolved public name equals this group name.
			desired = mergeUniqueStrings(desired, modelsResolvedToPublic(channelModelNames, group.Name, aliasIdx, config.normalize))
		}
		if err := reconcileGroupItemsForChannel(group, channel.ID, desired, ctx); err != nil {
			log.Warnf("auto group reconcile failed (channel=%d group=%d): %v", channel.ID, group.ID, err)
		}
	}

	if autoGroup == model.AutoGroupTypeExact && config.createMissing {
		if err := ensureMissingExactGroups(channel.ID, channelModelNames, groups, config.normalize, ctx); err != nil {
			log.Warnf("auto group create-missing failed (channel=%d): %v", channel.ID, err)
		}
	}
}

func modelsResolvedToPublic(channelModelNames []string, publicName string, idx *publicAliasIndex, useNorm bool) []string {
	if idx == nil || strings.TrimSpace(publicName) == "" {
		return nil
	}
	out := make([]string, 0)
	for _, up := range channelModelNames {
		pub, via := ResolvePublicModelName(up, idx, useNorm)
		if via == "none" || via == "normalize_local" {
			// normalize_local alone is not dictionary resolution for matching existing groups
			// (PublicModelNamesMatch already covers normalize to group name).
			if via == "normalize_local" && PublicModelNamesMatch(up, publicName, true) {
				out = append(out, up)
			}
			continue
		}
		if strings.EqualFold(pub, publicName) {
			out = append(out, up)
		}
	}
	return out
}

func mergeUniqueStrings(base, extra []string) []string {
	if len(extra) == 0 {
		return base
	}
	seen := make(map[string]struct{}, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, s := range base {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, s := range extra {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// AutoGroupCreateMissingEnabled reports whether exact-match auto-group may
// create groups for models that do not yet have a matching group name.
func AutoGroupCreateMissingEnabled() bool {
	enabled, err := SettingGetBool(model.SettingKeyAutoGroupCreateMissingEnabled)
	if err != nil {
		return false
	}
	return enabled
}

// AutoGroupNormalizeEnabled reports whether exact-match compares/creates
// groups using NormalizePublicModelName (strip provider prefix + date suffix).
func AutoGroupNormalizeEnabled() bool {
	enabled, err := SettingGetBool(model.SettingKeyAutoGroupNormalizeEnabled)
	if err != nil {
		return false
	}
	return enabled
}

// ensureMissingExactGroups creates RoundRobin groups for models that have no
// existing group match. With normalize enabled, the group name uses the
// normalized public form (e.g. gpt-4o-2024-08-06 → gpt-4o) while the group
// item keeps the original upstream model id.
func ensureMissingExactGroups(channelID int, channelModelNames []string, existingGroups []model.Group, normalize bool, ctx context.Context) error {
	if channelID <= 0 || len(channelModelNames) == 0 {
		return nil
	}
	aliasIdx, _ := loadPublicAliasIndex(ctx)

	existingByLower := make(map[string]struct{}, len(existingGroups)*2)
	for _, group := range existingGroups {
		name := strings.TrimSpace(group.Name)
		if name == "" {
			continue
		}
		existingByLower[strings.ToLower(name)] = struct{}{}
		if normalize {
			if n := NormalizePublicModelName(name); n != "" {
				existingByLower[strings.ToLower(n)] = struct{}{}
			}
		}
	}

	// public group name (lower) → first original model that needs attaching
	type pendingCreate struct {
		groupName  string
		modelNames []string
	}
	pendingByLower := make(map[string]*pendingCreate)

	for _, modelName := range channelModelNames {
		trimmed := strings.TrimSpace(modelName)
		if trimmed == "" {
			continue
		}
		// Already matched an existing group via exact/normalize reconcile.
		matchedExisting := false
		for _, group := range existingGroups {
			if PublicModelNamesMatch(trimmed, group.Name, normalize) {
				matchedExisting = true
				break
			}
		}
		if matchedExisting {
			continue
		}

		// Prefer alias dictionary public name when resolving create-missing.
		groupName := trimmed
		if aliasIdx != nil {
			if pub, via := ResolvePublicModelName(trimmed, aliasIdx, normalize); via != "none" && pub != "" {
				if via != "normalize_local" || len(aliasIdx.byExact) == 0 {
					groupName = pub
				} else if normalize {
					if n := NormalizePublicModelName(trimmed); n != "" {
						groupName = n
					}
				}
			} else if normalize {
				if n := NormalizePublicModelName(trimmed); n != "" {
					groupName = n
				}
			}
		} else if normalize {
			if n := NormalizePublicModelName(trimmed); n != "" {
				groupName = n
			}
		}
		lower := strings.ToLower(groupName)
		if _, ok := existingByLower[lower]; ok {
			continue
		}
		if entry, ok := pendingByLower[lower]; ok {
			// Multiple upstream ids collapse to the same public group.
			dup := false
			for _, have := range entry.modelNames {
				if have == trimmed {
					dup = true
					break
				}
			}
			if !dup {
				entry.modelNames = append(entry.modelNames, trimmed)
			}
			continue
		}
		pendingByLower[lower] = &pendingCreate{
			groupName:  groupName,
			modelNames: []string{trimmed},
		}
	}

	for _, entry := range pendingByLower {
		group := &model.Group{
			Name: entry.groupName,
			Mode: model.GroupModeRoundRobin,
		}
		if err := GroupCreate(group, ctx); err != nil {
			if existing, getErr := GroupGetEnabledMap(entry.groupName, ctx); getErr == nil && existing.ID > 0 {
				items := make([]model.GroupIDAndLLMName, 0, len(entry.modelNames))
				for _, mn := range entry.modelNames {
					items = append(items, model.GroupIDAndLLMName{ChannelID: channelID, ModelName: mn})
				}
				if addErr := GroupItemBatchAdd(existing.ID, items, ctx); addErr != nil {
					return addErr
				}
				existingByLower[strings.ToLower(entry.groupName)] = struct{}{}
				continue
			}
			return err
		}
		items := make([]model.GroupIDAndLLMName, 0, len(entry.modelNames))
		for _, mn := range entry.modelNames {
			items = append(items, model.GroupIDAndLLMName{ChannelID: channelID, ModelName: mn})
		}
		if err := GroupItemBatchAdd(group.ID, items, ctx); err != nil {
			return err
		}
		existingByLower[strings.ToLower(entry.groupName)] = struct{}{}
	}
	return nil
}

// matchModelsForAutoGroup returns channel models that should belong to group
// under the given auto-group mode. ok=false means the rule could not be applied
// (skip reconcile; do not wipe existing membership).
func matchModelsForAutoGroup(autoGroup model.AutoGroupType, group model.Group, channelModelNames []string, channelID int, normalize bool) (matched []string, ok bool) {
	matchedModelNames := make([]string, 0)
	switch autoGroup {
	case model.AutoGroupTypeExact:
		for _, modelName := range channelModelNames {
			if PublicModelNamesMatch(modelName, group.Name, normalize) {
				matchedModelNames = append(matchedModelNames, modelName)
			}
		}
		return matchedModelNames, true
	case model.AutoGroupTypeFuzzy:
		groupNameLower := strings.ToLower(strings.TrimSpace(group.Name))
		if groupNameLower == "" {
			return nil, true
		}
		for _, modelName := range channelModelNames {
			if strings.Contains(strings.ToLower(modelName), groupNameLower) {
				matchedModelNames = append(matchedModelNames, modelName)
			}
		}
		return matchedModelNames, true
	case model.AutoGroupTypeRegex:
		if group.MatchRegex == "" {
			for _, modelName := range channelModelNames {
				if strings.EqualFold(modelName, group.Name) {
					matchedModelNames = append(matchedModelNames, modelName)
				}
			}
			return matchedModelNames, true
		}

		re, err := regexp2.Compile(group.MatchRegex, regexp2.ECMAScript)
		if err != nil {
			log.Warnf("compile regex failed (channel=%d group=%d regex=%q): %v", channelID, group.ID, group.MatchRegex, err)
			return nil, false
		}
		re.MatchTimeout = 200 * time.Millisecond
		for _, modelName := range channelModelNames {
			matched, err := re.MatchString(modelName)
			if err != nil {
				log.Warnf("match regex failed (channel=%d group=%d regex=%q model=%q): %v", channelID, group.ID, group.MatchRegex, modelName, err)
				continue
			}
			if matched {
				matchedModelNames = append(matchedModelNames, modelName)
			}
		}
		return matchedModelNames, true
	default:
		return nil, false
	}
}

// reconcileGroupItemsForChannel applies desired set for one channel inside one group.
func reconcileGroupItemsForChannel(group model.Group, channelID int, desired []string, ctx context.Context) error {
	desiredSet := make(map[string]struct{}, len(desired))
	for _, name := range desired {
		desiredSet[name] = struct{}{}
	}

	existing := make([]string, 0)
	for _, item := range group.Items {
		if item.ChannelID != channelID {
			continue
		}
		existing = append(existing, item.ModelName)
	}

	toAdd := make([]model.GroupIDAndLLMName, 0)
	for _, name := range desired {
		found := false
		for _, have := range existing {
			if have == name {
				found = true
				break
			}
		}
		if !found {
			toAdd = append(toAdd, model.GroupIDAndLLMName{ChannelID: channelID, ModelName: name})
		}
	}

	toRemove := make([]string, 0)
	for _, have := range existing {
		if _, ok := desiredSet[have]; !ok {
			toRemove = append(toRemove, have)
		}
	}

	if len(toRemove) > 0 {
		if err := GroupItemBatchDelInGroup(group.ID, channelID, toRemove, ctx); err != nil {
			return err
		}
	}
	if len(toAdd) > 0 {
		if err := GroupItemBatchAdd(group.ID, toAdd, ctx); err != nil {
			return err
		}
	}
	return nil
}

func ChannelAutoGroup(channel *model.Channel, ctx context.Context) {
	if channel == nil {
		return
	}
	routeGroups, err := RouteGroupList(ctx)
	if err != nil {
		log.Warnf("list route groups for auto group failed: %v", err)
		return
	}
	_, managed, err := ChannelManagedBinding(channel.ID, ctx)
	if err != nil {
		log.Warnf("load channel binding for auto group failed: %v", err)
		return
	}
	for _, routeGroup := range routeGroups {
		scopedCtx := WithRouteGroup(ctx, routeGroup.ID)
		config, err := loadRouteAutoGroupConfig(scopedCtx)
		if err != nil {
			log.Warnf("load auto group config (route_group=%d) failed: %v", routeGroup.ID, err)
			continue
		}
		if mode := config.effectiveMode(*channel, managed); mode != model.AutoGroupTypeNone {
			channelAutoGroupWithConfig(channel, mode, config, scopedCtx)
		}
	}
}

func AutoGroupAllProjectedChannels(ctx context.Context) error {
	config, err := loadRouteAutoGroupConfig(ctx)
	if err != nil {
		return err
	}
	mode := config.projectedMode
	if mode == model.AutoGroupTypeNone {
		return nil
	}
	channels := channelCache.GetAll()
	if len(channels) == 0 {
		return nil
	}
	channelIDs := make([]int, 0, len(channels))
	for id := range channels {
		channelIDs = append(channelIDs, id)
	}
	bindingMap, err := SiteChannelBindingMapByChannelIDs(channelIDs, ctx)
	if err != nil {
		return err
	}
	for id, channel := range channels {
		if _, ok := bindingMap[id]; !ok {
			continue
		}
		channelAutoGroupWithConfig(&channel, mode, config, ctx)
	}
	return nil
}

func splitChannelModelNames(values ...string) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0)
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			name := strings.TrimSpace(part)
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			result = append(result, name)
		}
	}
	return result
}

func ValidateJSONOverrideObject(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return err
	}
	if _, ok := decoded.(map[string]any); !ok {
		return fmt.Errorf("param_override must be a JSON object")
	}
	return nil
}
