package update

import (
	"testing"

	"github.com/xuanli27/octopus/internal/conf"
)

func TestUpdateRepository(test *testing.T) {
	testCases := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "repository metadata",
			got:  conf.Repo,
			want: "https://github.com/Bduoluoluo/octopus",
		},
		{
			name: "release information",
			got:  updateApiUrl,
			want: "https://api.github.com/repos/Bduoluoluo/octopus/releases/latest",
		},
		{
			name: "release downloads",
			got:  updateUrl,
			want: "https://github.com/Bduoluoluo/octopus/releases/latest/download",
		},
	}

	for _, testCase := range testCases {
		test.Run(testCase.name, func(test *testing.T) {
			if testCase.got != testCase.want {
				test.Errorf("repository URL = %q, want %q", testCase.got, testCase.want)
			}
		})
	}
}
