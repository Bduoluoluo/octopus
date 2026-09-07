package model

import "testing"

func TestLogStorageLimitValidation(test *testing.T) {
	for _, value := range []string{"1", "1024", "1048576"} {
		setting := Setting{Key: SettingKeyRelayLogMaxDBSizeMB, Value: value}
		if err := setting.Validate(); err != nil {
			test.Errorf("valid limit %q rejected: %v", value, err)
		}
	}
	for _, value := range []string{"", "0", "-1", "1.5", "1048577", "invalid"} {
		setting := Setting{Key: SettingKeyRelayLogMaxDBSizeMB, Value: value}
		if err := setting.Validate(); err == nil {
			test.Errorf("invalid limit %q accepted", value)
		}
	}
	for _, setting := range DefaultSettings() {
		if setting.Key == SettingKeyRelayLogMaxDBSizeMB {
			if setting.Value != "1024" {
				test.Errorf("default limit = %q, want 1024", setting.Value)
			}
			return
		}
	}
	test.Fatal("default storage limit missing")
}
