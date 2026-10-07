package op

import (
	"testing"

	"github.com/xuanli27/octopus/internal/model"
)

func TestChannelPromptSuffixPersistence(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	if err := channelRefreshCache(ctx); err != nil {
		t.Fatal(err)
	}
	suffix := "\n\nfirst\n"
	channel := &model.Channel{Name: "suffix", PromptSuffix: &suffix}
	if err := ChannelCreate(channel, ctx); err != nil {
		t.Fatal(err)
	}
	assertSaved := func(expected string) {
		t.Helper()
		if err := channelRefreshCache(ctx); err != nil {
			t.Fatal(err)
		}
		reloaded, err := ChannelGet(channel.ID, ctx)
		if err != nil {
			t.Fatal(err)
		}
		if reloaded.PromptSuffix == nil || *reloaded.PromptSuffix != expected {
			t.Fatalf("expected suffix %q, got %+v", expected, reloaded)
		}
	}
	assertSaved(suffix)
	name := "renamed"
	if _, err := ChannelUpdate(&model.ChannelUpdateRequest{ID: channel.ID, Name: &name}, ctx); err != nil {
		t.Fatal(err)
	}
	assertSaved(suffix)
	for _, updated := range []string{"\nupdated ", ""} {
		if _, err := ChannelUpdate(&model.ChannelUpdateRequest{ID: channel.ID, PromptSuffix: &updated}, ctx); err != nil {
			t.Fatal(err)
		}
		assertSaved(updated)
	}
}
