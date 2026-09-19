package model

import (
	"errors"
	"math"
)

var ErrInvalidCacheRatio = errors.New("cache ratio must satisfy 0 <= minimum <= maximum <= 100")

func (channel *Channel) ValidateCacheRatio() error {
	if math.IsNaN(channel.CacheRatioMin) || math.IsNaN(channel.CacheRatioMax) ||
		math.IsInf(channel.CacheRatioMin, 0) || math.IsInf(channel.CacheRatioMax, 0) ||
		channel.CacheRatioMin < 0 || channel.CacheRatioMax > 100 || channel.CacheRatioMin > channel.CacheRatioMax {
		return ErrInvalidCacheRatio
	}
	return nil
}
