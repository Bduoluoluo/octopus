package model

import (
	"testing"
	"time"
)

func TestGetChannelKeyPrefersPreferredKeyID(t *testing.T) {
	channel := &Channel{
		Keys: []ChannelKey{
			{ID: 1, Enabled: true, ChannelKey: "first", TotalCost: 1},
			{ID: 2, Enabled: true, ChannelKey: "preferred", TotalCost: 100},
		},
	}

	selected := channel.GetChannelKey(ChannelKeySelectOptions{PreferredKeyID: 2})
	if selected.ID != 2 {
		t.Fatalf("expected preferred key 2, got %d", selected.ID)
	}
}

func TestGetChannelKeyUsesPreferredKeyAfterRecent429(t *testing.T) {
	channel := &Channel{
		Keys: []ChannelKey{
			{ID: 1, Enabled: true, ChannelKey: "fallback", TotalCost: 1},
			{ID: 2, Enabled: true, ChannelKey: "preferred", TotalCost: 100, StatusCode: 429, LastUseTimeStamp: time.Now().Unix()},
		},
	}

	selected := channel.GetChannelKey(ChannelKeySelectOptions{PreferredKeyID: 2})
	if selected.ID != 2 {
		t.Fatalf("expected preferred key 2 despite recent 429, got %d", selected.ID)
	}
}

func TestGetChannelKeyUsesLowestIDKey(t *testing.T) {
	channel := &Channel{
		Keys: []ChannelKey{
			{ID: 9, Enabled: true, ChannelKey: "higher-id", TotalCost: 1},
			{ID: 3, Enabled: true, ChannelKey: "lower-id", TotalCost: 100},
			{ID: 1, Enabled: false, ChannelKey: "disabled-lowest-id", TotalCost: 0},
			{ID: 2, Enabled: true, ChannelKey: "", TotalCost: 0},
		},
	}

	selected := channel.GetChannelKey()
	if selected.ID != 3 {
		t.Fatalf("expected lowest enabled non-empty key ID 3, got %d", selected.ID)
	}
}
