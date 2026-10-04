package op

import (
	"context"
	"strconv"

	"github.com/xuanli27/octopus/internal/db"
	"github.com/xuanli27/octopus/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type routeAutoGroupConfig struct {
	routeGroupID  int
	projectedMode model.AutoGroupType
	createMissing bool
	normalize     bool
	channelModes  map[int]model.AutoGroupType
}

func loadRouteAutoGroupConfig(ctx context.Context) (*routeAutoGroupConfig, error) {
	var group model.RouteGroup
	if err := db.GetDB().WithContext(ctx).First(&group, RouteGroupIDFromContext(ctx)).Error; err != nil {
		return nil, err
	}
	config := &routeAutoGroupConfig{
		routeGroupID:  group.ID,
		projectedMode: group.ProjectedAutoGroup,
		createMissing: group.CreateMissingGroups,
		normalize:     group.NormalizeModelNames,
		channelModes:  make(map[int]model.AutoGroupType),
	}
	if group.ID == model.DefaultRouteGroupID {
		config.projectedMode = ProjectedChannelGlobalAutoGroupMode()
		config.createMissing = AutoGroupCreateMissingEnabled()
		config.normalize = AutoGroupNormalizeEnabled()
		return config, nil
	}
	var channels []model.RouteGroupChannel
	if err := db.GetDB().WithContext(ctx).Where("route_group_id = ?", group.ID).Find(&channels).Error; err != nil {
		return nil, err
	}
	for _, channel := range channels {
		config.channelModes[channel.ChannelID] = channel.AutoGroup
	}
	return config, nil
}

func (config *routeAutoGroupConfig) channelMode(channel model.Channel) model.AutoGroupType {
	if config.routeGroupID == model.DefaultRouteGroupID {
		return channel.AutoGroup
	}
	return config.channelModes[channel.ID]
}

func (config *routeAutoGroupConfig) effectiveMode(channel model.Channel, managed bool) model.AutoGroupType {
	if managed && config.projectedMode != model.AutoGroupTypeNone {
		return config.projectedMode
	}
	return config.channelMode(channel)
}

func saveRouteAutoGroupConfig(req *model.GroupAutoGroupConfigUpdateRequest, ctx context.Context) error {
	routeGroupMutationMu.Lock()
	defer routeGroupMutationMu.Unlock()
	routeGroupID := RouteGroupIDFromContext(ctx)
	settings := make(map[model.SettingKey]string)
	err := db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := validateRouteGroup(tx, routeGroupID); err != nil {
			return err
		}
		if routeGroupID == model.DefaultRouteGroupID {
			if req.ProjectedGlobalAutoGroup != nil {
				settings[model.SettingKeyProjectedChannelAutoGroupEnabled] = strconv.Itoa(int(*req.ProjectedGlobalAutoGroup))
			}
			if req.CreateMissingGroups != nil {
				settings[model.SettingKeyAutoGroupCreateMissingEnabled] = strconv.FormatBool(*req.CreateMissingGroups)
			}
			if req.NormalizeModelNames != nil {
				settings[model.SettingKeyAutoGroupNormalizeEnabled] = strconv.FormatBool(*req.NormalizeModelNames)
			}
			for key, value := range settings {
				if err := tx.Save(&model.Setting{Key: key, Value: value}).Error; err != nil {
					return err
				}
			}
		} else {
			updates := make(map[string]any)
			if req.ProjectedGlobalAutoGroup != nil {
				updates["projected_auto_group"] = *req.ProjectedGlobalAutoGroup
			}
			if req.CreateMissingGroups != nil {
				updates["create_missing_groups"] = *req.CreateMissingGroups
			}
			if req.NormalizeModelNames != nil {
				updates["normalize_model_names"] = *req.NormalizeModelNames
			}
			if len(updates) > 0 {
				if err := tx.Model(&model.RouteGroup{}).Where("id = ?", routeGroupID).Updates(updates).Error; err != nil {
					return err
				}
			}
		}
		for _, item := range req.Items {
			if item.AutoGroup == nil {
				continue
			}
			var channel model.Channel
			if err := tx.Select("id").First(&channel, item.ChannelID).Error; err != nil {
				return err
			}
			if routeGroupID == model.DefaultRouteGroupID {
				if err := tx.Model(&model.Channel{}).Where("id = ?", item.ChannelID).Update("auto_group", *item.AutoGroup).Error; err != nil {
					return err
				}
			} else {
				entry := model.RouteGroupChannel{RouteGroupID: routeGroupID, ChannelID: item.ChannelID, AutoGroup: *item.AutoGroup}
				if err := tx.Clauses(clause.OnConflict{
					Columns:   []clause.Column{{Name: "route_group_id"}, {Name: "channel_id"}},
					DoUpdates: clause.AssignmentColumns([]string{"auto_group"}),
				}).Create(&entry).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for key, value := range settings {
		settingCache.Set(key, value)
	}
	if routeGroupID == model.DefaultRouteGroupID {
		for _, item := range req.Items {
			if item.AutoGroup != nil {
				if err := channelRefreshCacheByID(item.ChannelID, ctx); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
