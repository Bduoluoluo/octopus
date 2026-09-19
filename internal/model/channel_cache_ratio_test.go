package model

import (
	"math"
	"testing"
)

func TestChannelCacheRatioValidation(t *testing.T) {
	for _, test := range []struct {
		minimum, maximum float64
		valid            bool
	}{
		{0, 0, true}, {0, 100, true}, {27.5, 80.25, true}, {100, 100, true},
		{-1, 20, false}, {50, 20, false}, {0, 101, false},
		{math.NaN(), 100, false}, {0, math.Inf(1), false},
	} {
		channel := Channel{CacheRatioMin: test.minimum, CacheRatioMax: test.maximum}
		if err := channel.ValidateCacheRatio(); (err == nil) != test.valid {
			t.Errorf("range [%v, %v]: %v", test.minimum, test.maximum, err)
		}
	}
}
