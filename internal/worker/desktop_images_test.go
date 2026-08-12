package worker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"

	"go.uber.org/zap"

	"github.com/stretchr/testify/require"
)

func TestDesktopImagesFromTurnAccepts960x540DataURL(t *testing.T) {
	var encoded bytes.Buffer
	picture := image.NewRGBA(image.Rect(0, 0, 960, 540))
	for y := 0; y < picture.Bounds().Dy(); y++ {
		for x := 0; x < picture.Bounds().Dx(); x++ {
			picture.SetRGBA(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256),
				B: uint8((x + y) % 256), A: 255})
		}
	}
	require.NoError(t, png.Encode(&encoded, picture))
	params, err := json.Marshal(map[string]any{"input": []map[string]string{{
		"type": "image", "url": "data:image/png;base64," +
			base64.StdEncoding.EncodeToString(encoded.Bytes()),
	}}})
	require.NoError(t, err)

	images, notice, err := desktopImagesFromTurn(context.Background(), params,
		openLocalDesktopImage)
	require.NoError(t, err)
	require.Empty(t, notice)
	require.Len(t, images, 1)
	actual := images[0]
	require.Empty(t, actual.Error)
	require.Equal(t, "image/png", actual.MediaType)
	require.Equal(t, int64(encoded.Len()), actual.Size)
	require.True(t, actual.Temporary)
	file, err := os.Open(actual.SourcePath)
	require.NoError(t, err)
	configuration, _, err := image.DecodeConfig(file)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	require.Equal(t, 960, configuration.Width)
	require.Equal(t, 540, configuration.Height)

	cleanupTemporaryDesktopImages(images, zap.NewNop())
	_, err = os.Stat(actual.SourcePath)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestDesktopImagesFromTurnRejectsInvalidDataURLs(t *testing.T) {
	tests := []string{
		"https://example.com/image.png",
		"data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=",
		"data:image/png;base64,not-base64",
		"data:image/jpeg;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB",
	}
	for _, raw := range tests {
		params, err := json.Marshal(map[string]any{"input": []map[string]string{{
			"type": "image", "url": raw,
		}}})
		require.NoError(t, err)
		images, _, err := desktopImagesFromTurn(context.Background(), params,
			openLocalDesktopImage)
		require.NoError(t, err)
		require.Len(t, images, 1)
		require.NotEmpty(t, images[0].Error)
		require.Empty(t, images[0].SourcePath)
	}
}
