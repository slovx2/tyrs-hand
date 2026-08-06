package worker

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

const agentImageTestPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

func TestLocalMarkdownImagesCollectsLocalAndPreservesRenderableExternalSources(t *testing.T) {
	workspace := t.TempDir()
	imageBytes, err := base64.StdEncoding.DecodeString(agentImageTestPNG)
	require.NoError(t, err)
	localPath := filepath.Join(workspace, "result.png")
	require.NoError(t, os.WriteFile(localPath, imageBytes, 0o600))
	markdown := "before\n\n![relative](result.png)\n" +
		"![absolute](" + localPath + ")\n" +
		"![web](https://example.com/result.png)\n" +
		"![data](data:image/png;base64," + agentImageTestPNG + ")\n" +
		"![content](content://media/external/1)\n" +
		"![missing](missing.png)\n\nafter"

	cleaned, candidates := localMarkdownImages(markdown, workspace)

	require.Len(t, candidates, 2)
	require.Equal(t, localPath, candidates[0].path)
	require.Equal(t, localPath, candidates[1].path)
	require.NotContains(t, cleaned, "![relative]")
	require.NotContains(t, cleaned, "![absolute]")
	require.NotContains(t, cleaned, "![missing]")
	require.Contains(t, cleaned, "![web](https://example.com/result.png)")
	require.Contains(t, cleaned, "![data](data:image/png;base64,")
	require.Contains(t, cleaned, "![content](content://media/external/1)")
}

func TestValidateAgentImageChecksTypeSizeAndDigest(t *testing.T) {
	imageBytes, err := base64.StdEncoding.DecodeString(agentImageTestPNG)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "image.png")
	require.NoError(t, os.WriteFile(path, imageBytes, 0o600))

	resolved, digest, err := validateAgentImage(path)
	require.NoError(t, err)
	require.Equal(t, path, resolved)
	require.Len(t, digest, 64)

	invalid := filepath.Join(t.TempDir(), "not-image.png")
	require.NoError(t, os.WriteFile(invalid, []byte("not an image"), 0o600))
	_, _, err = validateAgentImage(invalid)
	require.ErrorContains(t, err, "只支持")
}

func TestUploadAgentAttachmentRetriesTransientControlFailureWithSameIdempotencyKey(t *testing.T) {
	var requests atomic.Int32
	attachmentID := uuid.New()
	runID := uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/worker/v1/runs/"+runID.String()+"/attachments", request.URL.Path)
		require.NoError(t, request.ParseMultipartForm(1<<20))
		require.Equal(t, "item-1", request.FormValue("itemId"))
		require.Equal(t, "2", request.FormValue("ordinal"))
		if requests.Add(1) < 3 {
			http.Error(w, "temporary unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"attachmentId":%q,"deduplicated":true}`, attachmentID)
	}))
	defer server.Close()

	imageBytes, err := base64.StdEncoding.DecodeString(agentImageTestPNG)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "image.png")
	require.NoError(t, os.WriteFile(path, imageBytes, 0o600))
	client := workerprotocol.NewClient(server.URL, "worker-token", time.Second)
	processor := &Processor{cfg: config.Config{ControlTimeout: time.Second}, client: client}
	task := &workerprotocol.Task{}
	task.Claimed.RunID = runID
	task.Claimed.LeaseToken = "lease-token"
	task.Claimed.LeaseEpoch = 4

	result, err := processor.uploadAgentAttachment(context.Background(), task, "item-1", 2, path)

	require.NoError(t, err)
	require.Equal(t, attachmentID, result.AttachmentID)
	require.True(t, result.Deduplicated)
	require.EqualValues(t, 3, requests.Load())
}

func TestRetryableControlErrorRejectsPermanentHTTPFailure(t *testing.T) {
	require.False(t, retryableControlError(&workerprotocol.HTTPError{
		StatusCode: http.StatusBadRequest, Status: "400 Bad Request",
	}))
	require.True(t, retryableControlError(&workerprotocol.HTTPError{
		StatusCode: http.StatusTooManyRequests, Status: "429 Too Many Requests",
	}))
	require.True(t, retryableControlError(&workerprotocol.HTTPError{
		StatusCode: http.StatusBadGateway, Status: "502 Bad Gateway",
	}))
}
