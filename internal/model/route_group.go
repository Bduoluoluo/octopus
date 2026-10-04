package model

const DefaultRouteGroupID = 1

type RouteGroup struct {
	ID                  int           `json:"id" gorm:"primaryKey"`
	Name                string        `json:"name" gorm:"size:100;not null;uniqueIndex"`
	ProjectedAutoGroup  AutoGroupType `json:"projected_auto_group" gorm:"not null;default:0"`
	CreateMissingGroups bool          `json:"create_missing_groups" gorm:"not null;default:false"`
	NormalizeModelNames bool          `json:"normalize_model_names" gorm:"not null;default:false"`
}

type RouteGroupChannel struct {
	RouteGroupID int           `json:"route_group_id" gorm:"primaryKey;autoIncrement:false"`
	ChannelID    int           `json:"channel_id" gorm:"primaryKey;autoIncrement:false;index"`
	AutoGroup    AutoGroupType `json:"auto_group" gorm:"not null;default:0"`
}
