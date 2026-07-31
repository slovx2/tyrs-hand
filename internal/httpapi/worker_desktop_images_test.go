package httpapi

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestPrepareDesktopImagesKeepsOnlyMetadata(t *testing.T) {
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte("image")))
	images, failures := prepareDesktopImages([]workerprotocol.DesktopImage{
		{Filename: "截图.png", MediaType: "image/png", Size: 5, SHA256: digest},
		{Filename: "fake.jpg", MediaType: "image/png", Size: 5, SHA256: digest},
	}, "one skipped")

	require.Len(t, images, 2)
	require.Equal(t, "pending", images[0].status)
	require.Equal(t, "01-"+digest[:12]+"-image.png", images[0].discordFilename)
	require.Equal(t, "failed", images[1].status)
	require.Len(t, failures, 2)
	require.Contains(t, failures[0], "元数据无效")
	require.Equal(t, "one skipped", failures[1])
}

func TestValidatedDesktopImageReaderChecksSizeAndDigest(t *testing.T) {
	content := []byte("complete image")
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	read := func(source []byte, size int64, expected string) error {
		reader := &validatedDesktopImageReader{source: bytes.NewReader(source),
			remaining: size, digest: sha256.New(), expected: expected}
		_, err := io.ReadAll(reader)
		return err
	}

	require.NoError(t, read(content, int64(len(content)), digest))
	require.ErrorContains(t, read(content[:5], int64(len(content)), digest), "unexpected EOF")
	require.ErrorContains(t, read(append(content, 'x'), int64(len(content)), digest), "大小")
	require.ErrorContains(t, read(content, int64(len(content)), fmt.Sprintf("%064x", 1)),
		"SHA-256")
}
