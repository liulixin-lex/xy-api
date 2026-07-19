package service

import (
	"image"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetImageTokenPatchDimensionsDoNotOverflowInt(t *testing.T) {
	originalGetMediaToken := constant.GetMediaToken
	constant.GetMediaToken = true
	t.Cleanup(func() {
		constant.GetMediaToken = originalGetMediaToken
	})

	maxInt := int(^uint(0) >> 1)
	tests := []struct {
		name       string
		width      int
		height     int
		wantTokens int
	}{
		{
			name:       "single patch",
			width:      32,
			height:     32,
			wantTokens: 2,
		},
		{
			name:       "below cap",
			width:      1024,
			height:     1024,
			wantTokens: 1659,
		},
		{
			name:       "maximum square dimensions",
			width:      maxInt,
			height:     maxInt,
			wantTokens: 2464,
		},
		{
			name:       "maximum aspect ratio",
			width:      maxInt,
			height:     1,
			wantTokens: 2488,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			source := types.NewBase64FileSource("", "image/png")
			cachedData := types.NewMemoryCachedData("", "image/png", 0)
			config := image.Config{Width: test.width, Height: test.height}
			cachedData.ImageConfig = &config
			cachedData.ImageFormat = "png"
			source.SetCache(cachedData)
			t.Cleanup(func() {
				CleanupFileSources(ctx)
			})

			tokens, err := getImageToken(
				ctx,
				types.NewImageFileMeta(source, "high"),
				"gpt-4.1-mini",
				true,
			)
			require.NoError(t, err)
			assert.Equal(t, test.wantTokens, tokens)
			assert.Greater(t, tokens, 0)
		})
	}
}

func TestGetImageTokenTileDimensionsDoNotUnderflowOrOverflowInt(t *testing.T) {
	originalGetMediaToken := constant.GetMediaToken
	constant.GetMediaToken = true
	t.Cleanup(func() {
		constant.GetMediaToken = originalGetMediaToken
	})

	maxInt := int(^uint(0) >> 1)
	tests := []struct {
		name       string
		width      int
		height     int
		wantTokens int
	}{
		{
			name:       "maximum square dimensions",
			width:      maxInt,
			height:     maxInt,
			wantTokens: 25501,
		},
		{
			name:       "maximum horizontal aspect ratio",
			width:      maxInt,
			height:     1,
			wantTokens: 34820881,
		},
		{
			name:       "maximum vertical aspect ratio",
			width:      1,
			height:     maxInt,
			wantTokens: 34820881,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			source := types.NewBase64FileSource("", "image/png")
			cachedData := types.NewMemoryCachedData("", "image/png", 0)
			config := image.Config{Width: test.width, Height: test.height}
			cachedData.ImageConfig = &config
			cachedData.ImageFormat = "png"
			source.SetCache(cachedData)
			t.Cleanup(func() {
				CleanupFileSources(ctx)
			})

			tokens, err := getImageToken(
				ctx,
				types.NewImageFileMeta(source, "high"),
				"gpt-4o-mini",
				true,
			)
			require.NoError(t, err)
			assert.Equal(t, test.wantTokens, tokens)
			assert.Greater(t, tokens, 2833)
		})
	}
}

func TestGetImageTokenPreservesZeroDimensionFileFallback(t *testing.T) {
	originalGetMediaToken := constant.GetMediaToken
	constant.GetMediaToken = true
	t.Cleanup(func() {
		constant.GetMediaToken = originalGetMediaToken
	})

	for _, test := range []struct {
		name   string
		width  int
		height int
	}{
		{name: "zero width", width: 0, height: 32},
		{name: "zero height", width: 32, height: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			source := types.NewBase64FileSource("", "image/heic")
			cachedData := types.NewMemoryCachedData("", "image/heic", 0)
			config := image.Config{Width: test.width, Height: test.height}
			cachedData.ImageConfig = &config
			cachedData.ImageFormat = "heic"
			source.SetCache(cachedData)
			t.Cleanup(func() {
				CleanupFileSources(ctx)
			})

			tokens, err := getImageToken(
				ctx,
				types.NewImageFileMeta(source, "high"),
				"gpt-4.1-mini",
				true,
			)
			require.NoError(t, err)
			assert.Equal(t, 255, tokens)
		})
	}
}

func TestGetImageTokenRejectsNegativeDimensions(t *testing.T) {
	originalGetMediaToken := constant.GetMediaToken
	constant.GetMediaToken = true
	t.Cleanup(func() {
		constant.GetMediaToken = originalGetMediaToken
	})

	for _, test := range []struct {
		name   string
		width  int
		height int
	}{
		{name: "negative width", width: -1, height: 32},
		{name: "negative height", width: 32, height: -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			source := types.NewBase64FileSource("", "image/heic")
			cachedData := types.NewMemoryCachedData("", "image/heic", 0)
			config := image.Config{Width: test.width, Height: test.height}
			cachedData.ImageConfig = &config
			cachedData.ImageFormat = "heic"
			source.SetCache(cachedData)
			t.Cleanup(func() {
				CleanupFileSources(ctx)
			})

			tokens, err := getImageToken(
				ctx,
				types.NewImageFileMeta(source, "high"),
				"gpt-4.1-mini",
				true,
			)
			require.Error(t, err)
			assert.Zero(t, tokens)
			assert.Contains(t, err.Error(), "invalid image dimensions")
		})
	}
}
