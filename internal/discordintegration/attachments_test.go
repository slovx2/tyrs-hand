package discordintegration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestAttachmentDownloaderStreamsIntoWorkspace(t *testing.T) {
	content := []byte("package main\n")
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		require.Equal(t, "cdn.discordapp.com", request.URL.Host)
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{
			"Content-Type": []string{"text/plain; charset=utf-8"},
		}, Body: io.NopCloser(strings.NewReader(string(content))), Request: request}, nil
	})
	downloader := NewAttachmentDownloader(transport)
	workspace := t.TempDir()
	saved, err := downloader.Download(context.Background(), workspace, []AttachmentInput{{
		ID: "123", URL: "https://cdn.discordapp.com/attachments/1/2/main.go",
		Filename: "../../main.go", MediaType: "text/plain", Size: int64(len(content)),
	}})
	require.NoError(t, err)
	require.Len(t, saved, 1)
	require.Equal(t, "file", saved[0].Kind)
	require.Equal(t, "main.go", saved[0].Filename)
	require.NotContains(t, saved[0].RelativePath, "..")
	digest := sha256.Sum256(content)
	require.Equal(t, hex.EncodeToString(digest[:]), saved[0].SHA256)
	data, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(saved[0].RelativePath)))
	require.NoError(t, err)
	require.Equal(t, content, data)
}

func TestAttachmentDownloaderRejectsForbiddenRedirect(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 302, Status: "302 Found", Header: http.Header{
			"Location": []string{"https://example.com/stolen.txt"},
		}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	})
	downloader := NewAttachmentDownloader(transport)
	_, err := downloader.Download(context.Background(), t.TempDir(), []AttachmentInput{{
		ID: "1", URL: "https://cdn.discordapp.com/attachments/1/2/file.txt", Filename: "file.txt", MediaType: "text/plain",
	}})
	require.ErrorContains(t, err, "CDN")
}

func TestAttachmentDownloaderRejectsMIMEAndSizeMismatch(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{
			"Content-Type": []string{"application/pdf"},
		}, Body: io.NopCloser(strings.NewReader("not a png")), Request: request}, nil
	})
	downloader := NewAttachmentDownloader(transport)
	_, err := downloader.Download(context.Background(), t.TempDir(), []AttachmentInput{{
		ID: "1", URL: "https://cdn.discordapp.com/attachments/1/2/file.png", Filename: "file.png", MediaType: "image/png", Size: 9,
	}})
	require.ErrorContains(t, err, "Content-Type")

	downloader.maxFileBytes = 2
	_, err = downloader.Download(context.Background(), t.TempDir(), []AttachmentInput{{
		ID: "1", URL: "https://cdn.discordapp.com/attachments/1/2/file.txt", Filename: "file.txt", MediaType: "text/plain", Size: 3,
	}})
	require.ErrorContains(t, err, "大小")
}

func TestAttachmentDownloaderRejectsSymlinkEscape(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(workspace, ".tyrs-hand")))
	downloader := NewAttachmentDownloader(roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("x")), Request: request}, nil
	}))
	_, err := downloader.Download(context.Background(), workspace, nil)
	require.ErrorContains(t, err, "不在会话工作区")
}

func TestAttachmentValidationMatrix(t *testing.T) {
	kind, err := validateAttachmentType(".png", "image/png")
	require.NoError(t, err)
	require.Equal(t, "image", kind)
	kind, err = validateAttachmentType(".pdf", "application/pdf")
	require.NoError(t, err)
	require.Equal(t, "file", kind)
	_, err = validateAttachmentType(".pdf", "text/plain")
	require.Error(t, err)
	_, err = validateAttachmentType(".go", "application/zip")
	require.Error(t, err)
	_, err = validateAttachmentType(".exe", "application/octet-stream")
	require.Error(t, err)
	require.Equal(t, "application/json", normalizeMediaType("Application/JSON; charset=utf-8"))
	require.Empty(t, normalizeMediaType("not a mime;"))

	_, err = sanitizeFilename(strings.Repeat("a", 181) + ".txt")
	require.Error(t, err)
	require.Equal(t, "unsafe_name.txt", mustFilename(t, " unsafe name.txt "))
	d := NewAttachmentDownloader(nil)
	_, err = d.validateURL("https://user@example.com/file.txt")
	require.Error(t, err)
	_, err = d.validateURL("https://cdn.discordapp.com:8443/file.txt")
	require.Error(t, err)
	request, err := http.NewRequest(http.MethodGet, "https://cdn.discordapp.com/file.txt", nil)
	require.NoError(t, err)
	via := make([]*http.Request, 3)
	require.Error(t, d.checkRedirect(request, via))
}

func mustFilename(t *testing.T, value string) string {
	t.Helper()
	filename, err := sanitizeFilename(value)
	require.NoError(t, err)
	return filename
}
