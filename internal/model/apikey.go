package model

import "strings"

type APIKey struct {
	ID           int     `json:"id" gorm:"primaryKey"`
	Name         string  `json:"name" gorm:"not null"`
	RouteGroupID int     `json:"route_group_id" gorm:"not null;default:1;index"`
	APIKey       string  `json:"api_key" gorm:"not null"`
	Enabled      bool    `json:"enabled" gorm:"default:true"`
	ExpireAt     int64   `json:"expire_at,omitempty"`
	MaxCost      float64 `json:"max_cost,omitempty"`
	MaxRPM       int     `json:"max_rpm,omitempty"`
	// SupportedModels is a comma-separated list. Semantics depend on ModelListMode:
	//   allow (default): empty = all models; non-empty = whitelist
	//   deny: empty = all models; non-empty = blacklist (issue #102)
	SupportedModels string `json:"supported_models,omitempty"`
	// ModelListMode: "" / "allow" = whitelist; "deny" = blacklist.
	ModelListMode string `json:"model_list_mode,omitempty" gorm:"type:varchar(16);not null;default:''"`
}

// ModelAllowed reports whether modelName is permitted by this API key's list policy.
func (k *APIKey) ModelAllowed(modelName string) bool {
	if k == nil {
		return true
	}
	list := splitCommaModels(k.SupportedModels)
	if len(list) == 0 {
		return true
	}
	mode := strings.ToLower(strings.TrimSpace(k.ModelListMode))
	inList := false
	for _, m := range list {
		if m == modelName {
			inList = true
			break
		}
	}
	if mode == "deny" {
		return !inList
	}
	// allow (default)
	return inList
}

func splitCommaModels(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
