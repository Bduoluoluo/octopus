package op

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/xuanli27/octopus/internal/db"
	"github.com/xuanli27/octopus/internal/model"
	"gorm.io/gorm"
)

type routeGroupContextKey struct{}

var routeGroupMutationMu sync.Mutex

func WithRouteGroup(ctx context.Context, id int) context.Context {
	if id == 0 {
		id = model.DefaultRouteGroupID
	}
	return context.WithValue(ctx, routeGroupContextKey{}, id)
}

func RouteGroupIDFromContext(ctx context.Context) int {
	if id, ok := ctx.Value(routeGroupContextKey{}).(int); ok {
		return id
	}
	return model.DefaultRouteGroupID
}

func RouteGroupList(ctx context.Context) ([]model.RouteGroup, error) {
	groups := []model.RouteGroup{}
	err := db.GetDB().WithContext(ctx).Order("id ASC").Find(&groups).Error
	return groups, err
}

func validateRouteGroup(conn *gorm.DB, id int) error {
	if id <= 0 {
		return fmt.Errorf("invalid route group")
	}
	var group model.RouteGroup
	if err := conn.First(&group, id).Error; err != nil {
		return fmt.Errorf("route group not found: %w", err)
	}
	return nil
}

func RouteGroupCreate(name string, ctx context.Context) (*model.RouteGroup, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return nil, fmt.Errorf("route group name must contain 1 to 100 characters")
	}
	group := &model.RouteGroup{Name: name}
	if err := db.GetDB().WithContext(ctx).Create(group).Error; err != nil {
		return nil, err
	}
	return group, nil
}

func RouteGroupDelete(id int, ctx context.Context) error {
	if id == model.DefaultRouteGroupID {
		return fmt.Errorf("default route group cannot be deleted")
	}
	routeGroupMutationMu.Lock()
	defer routeGroupMutationMu.Unlock()
	var groups []model.Group
	err := db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := validateRouteGroup(tx, id); err != nil {
			return err
		}
		var boundKeys int64
		if err := tx.Model(&model.APIKey{}).Where("route_group_id = ?", id).Count(&boundKeys).Error; err != nil {
			return err
		}
		if boundKeys > 0 {
			return fmt.Errorf("route group is bound to %d API keys; reassign them before deleting", boundKeys)
		}
		if err := tx.Where("route_group_id = ?", id).Find(&groups).Error; err != nil {
			return err
		}
		groupIDs := tx.Model(&model.Group{}).Select("id").Where("route_group_id = ?", id)
		snapshotIDs := tx.Model(&model.GroupHealthSnapshot{}).Select("id").Where("group_id IN (?)", groupIDs)
		if err := tx.Where("snapshot_id IN (?)", snapshotIDs).Delete(&model.GroupHealthAttempt{}).Error; err != nil {
			return err
		}
		for _, table := range []any{&model.GroupHealthSnapshot{}, &model.GroupPreset{}, &model.GroupItem{}} {
			if err := tx.Where("group_id IN (?)", groupIDs).Delete(table).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("route_group_id = ?", id).Delete(&model.Group{}).Error; err != nil {
			return err
		}
		if err := tx.Where("route_group_id = ?", id).Delete(&model.RouteGroupChannel{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.RouteGroup{}, id).Error
	})
	if err != nil {
		return err
	}
	for _, group := range groups {
		groupCache.Del(group.ID)
		groupMap.Del(groupLookupKey(group.RouteGroupID, group.Name))
	}
	return nil
}

func groupLookupKey(routeGroupID int, name string) string {
	return fmt.Sprintf("%d/%s", routeGroupID, name)
}

func GroupListInRouteGroup(ctx context.Context) ([]model.Group, error) {
	groups, err := GroupList(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]model.Group, 0, len(groups))
	for _, group := range groups {
		if group.RouteGroupID == RouteGroupIDFromContext(ctx) {
			filtered = append(filtered, group)
		}
	}
	return filtered, nil
}
