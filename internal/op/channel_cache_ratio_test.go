package op

import (
	"errors"
	"testing"

	"github.com/xuanli27/octopus/internal/model"
)

func TestChannelCacheRatioPersistence(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	if err := channelRefreshCache(ctx); err != nil {
		t.Fatal(err)
	}
	channel := &model.Channel{Name: "cache-ratio", CacheRatioEnabled: true, CacheRatioMin: 25.5, CacheRatioMax: 75}
	if err := ChannelCreate(channel, ctx); err != nil {
		t.Fatal(err)
	}
	minimum := 80.0
	if _, err := ChannelUpdate(&model.ChannelUpdateRequest{ID: channel.ID, CacheRatioMin: &minimum}, ctx); !errors.Is(err, model.ErrInvalidCacheRatio) {
		t.Fatalf("expected invalid merged range, got %v", err)
	}
	minimum, maximum, enabled := 0.0, 0.0, false
	if _, err := ChannelUpdate(&model.ChannelUpdateRequest{ID: channel.ID, CacheRatioMin: &minimum, CacheRatioMax: &maximum, CacheRatioEnabled: &enabled}, ctx); err != nil {
		t.Fatal(err)
	}
	if err := channelRefreshCache(ctx); err != nil {
		t.Fatal(err)
	}
	reloaded, err := ChannelGet(channel.ID, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.CacheRatioEnabled || reloaded.CacheRatioMin != 0 || reloaded.CacheRatioMax != 0 {
		t.Fatalf("zero/false values were not saved: %+v", reloaded)
	}
	channel = &model.Channel{Name: "invalid-cache-ratio", CacheRatioMin: 101, CacheRatioMax: 100}
	if err := ChannelCreate(channel, ctx); !errors.Is(err, model.ErrInvalidCacheRatio) {
		t.Fatalf("invalid creation accepted: %v", err)
	}
}
