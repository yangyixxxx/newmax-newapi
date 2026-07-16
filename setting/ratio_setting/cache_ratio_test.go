package ratio_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitRatioSettingsIncludesNewMaxCacheRatios(t *testing.T) {
	original := GetCacheRatioCopy()
	t.Cleanup(func() {
		cacheRatioMap.Clear()
		cacheRatioMap.AddAll(original)
	})

	cacheRatioMap.Clear()
	InitRatioSettings()

	tests := []struct {
		model string
		ratio float64
	}{
		{model: "doubao-seed-2.1-pro", ratio: 0.2},
		{model: "doubao-seed-2-1-pro-260628", ratio: 0.2},
		{model: "doubao-seed-2.1-turbo", ratio: 0.2},
		{model: "doubao-seed-2-1-turbo-260628", ratio: 0.2},
		{model: "doubao-seed-evolving", ratio: 0.2},
		{model: "glm-5.2", ratio: 0.25},
		{model: "glm-5-2-260617", ratio: 0.25},
		{model: "deepseek-v4-flash", ratio: 0.2},
		{model: "deepseek-v4-flash-260425", ratio: 0.2},
		{model: "deepseek-v4-pro", ratio: 1.0 / 12.0},
		{model: "deepseek-v4-pro-260425", ratio: 1.0 / 12.0},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			ratio, configured := GetCacheRatio(tt.model)
			require.True(t, configured)
			assert.InDelta(t, tt.ratio, ratio, 1e-12)
		})
	}
}

func TestGetCacheRatioKeepsUnknownModelFallback(t *testing.T) {
	ratio, configured := GetCacheRatio("newmax-unknown-model")

	assert.False(t, configured)
	assert.Equal(t, 1.0, ratio)
}
