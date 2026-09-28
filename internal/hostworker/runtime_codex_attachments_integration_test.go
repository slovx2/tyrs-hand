//go:build integration

package hostworker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestRuntimeCodexAttachmentsRealSSH(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "codex-attachments")
}

type nativeAttachment struct {
	ID, AttachmentType, IdentityKey string
	Payload                         map[string]any
	CreatedAt                       int64
}

type nativeAttachmentResult struct {
	Outcome    string
	Attachment nativeAttachment
}

func nativeAttachments(t *testing.T, ctx context.Context, client *codex.SocketClient, threadID string) []nativeAttachment {
	t.Helper()
	var cursor *string
	items := []nativeAttachment{}
	seen := map[string]bool{}
	for page := 0; page < 10; page++ {
		result := nativeMetadataCall[struct {
			Data       []nativeAttachment
			NextCursor *string
		}](t, ctx, client, "thread/attachment/list", map[string]any{
			"threadId": threadID, "limit": 1, "cursor": cursor})
		require.LessOrEqual(t, len(result.Data), 1)
		for _, item := range result.Data {
			require.NotEmpty(t, item.ID)
			require.False(t, seen[item.ID], "原生附件分页不能重复")
			seen[item.ID] = true
			items = append(items, item)
		}
		cursor = result.NextCursor
		if cursor == nil {
			break
		}
	}
	require.Nil(t, cursor, "原生附件分页必须收敛")
	return items
}

func awaitNativeAttachmentNotice(t *testing.T, ctx context.Context, events *codex.EventSubscription, threadID string, item nativeAttachment, operation string) {
	t.Helper()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("未收到原生附件变更通知")
		case event, ok := <-events.Events():
			require.True(t, ok, "附件通知流不能提前关闭")
			if event.Method != "thread/attachment/updated" {
				continue
			}
			var notice struct{ ThreadID, AttachmentID, AttachmentType, IdentityKey, Operation string }
			require.NoError(t, json.Unmarshal(event.Params, &notice))
			require.Equal(t, threadID, notice.ThreadID)
			require.Equal(t, item.ID, notice.AttachmentID)
			require.Equal(t, item.AttachmentType, notice.AttachmentType)
			require.Equal(t, item.IdentityKey, notice.IdentityKey)
			require.Equal(t, operation, notice.Operation)
			return
		}
	}
}

// ATTACHMENT-001 只验收独立附件元数据；任意 payload 不等于图片上传或 fileId 下载。
func verifyCodexNativeAttachments(t *testing.T, ctx context.Context, client *codex.SocketClient, root string, registry *RuntimeRegistry, connection *ssh.Client) {
	t.Helper()
	path := filepath.Join(root, "project", "attachment.txt")
	writeNativeMetadataFile(t, path, "附件管理不能改写或删除源文件")
	start := func(text string) string {
		thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{
			"cwd": filepath.Join(root, "project"), "sandbox": "danger-full-access", "approvalPolicy": "never"})
		runNativeMetadataTurn(t, ctx, client, thread.ID, text)
		return thread.ID
	}
	first, other := start("ATTACHMENT_FIRST_HISTORY"), start("ATTACHMENT_OTHER_HISTORY")
	events := client.Subscribe(codex.ThreadFilter{})
	defer events.Close()
	require.Empty(t, nativeAttachments(t, ctx, client, first))
	require.Empty(t, nativeAttachments(t, ctx, client, other))
	add := func(threadID, kind, identity string, payload map[string]any) nativeAttachmentResult {
		return nativeMetadataCall[nativeAttachmentResult](t, ctx, client, "thread/attachment/add", map[string]any{
			"threadId": threadID, "attachmentType": kind, "identityKey": identity, "payload": payload})
	}
	payload := map[string]any{"path": path, "label": "本地附件", "metadata": map[string]any{"marker": "ATTACHMENT-001"}}
	original := add(first, "file", "same-key", payload)
	require.Equal(t, "created", original.Outcome)
	require.Equal(t, payload, original.Attachment.Payload)
	require.Positive(t, original.Attachment.CreatedAt)
	awaitNativeAttachmentNotice(t, ctx, events, first, original.Attachment, "created")
	repeated := add(first, "file", "same-key", map[string]any{"label": "不得覆盖"})
	require.Equal(t, "existing", repeated.Outcome)
	require.Equal(t, original.Attachment, repeated.Attachment, "相同身份不能创建第二条或改写原 payload")
	second := add(first, "file", "second-key", map[string]any{"label": "第二个附件"})
	require.Equal(t, "created", second.Outcome)
	awaitNativeAttachmentNotice(t, ctx, events, first, second.Attachment, "created")
	differentType := add(first, "reference", "same-key", map[string]any{"label": "另一类型"})
	require.Equal(t, "created", differentType.Outcome)
	awaitNativeAttachmentNotice(t, ctx, events, first, differentType.Attachment, "created")
	isolated := add(other, "file", "same-key", map[string]any{"label": "另一会话"})
	require.Equal(t, "created", isolated.Outcome)
	require.NotEqual(t, original.Attachment.ID, isolated.Attachment.ID)
	awaitNativeAttachmentNotice(t, ctx, events, other, isolated.Attachment, "created")
	before := nativeAttachments(t, ctx, client, first)
	require.ElementsMatch(t, []nativeAttachment{original.Attachment, second.Attachment, differentType.Attachment}, before)
	require.Equal(t, []nativeAttachment{isolated.Attachment}, nativeAttachments(t, ctx, client, other))
	otherGeneration := registry.entries[runtimeidentity.Claude].Runtime.Generation()
	require.NoError(t, registry.Restart(runtimeidentity.Codex))
	client = connectRuntimeSSH(t, ctx, connection, runtimeidentity.Codex)
	require.Equal(t, otherGeneration, registry.entries[runtimeidentity.Claude].Runtime.Generation())
	require.Equal(t, before, nativeAttachments(t, ctx, client, first), "重启必须保留原始身份、payload、时间和分页顺序")
	require.Equal(t, "existing", add(first, "file", "same-key", payload).Outcome)
	readSessionThread(t, ctx, client, "thread/resume", map[string]any{"threadId": first})
	deleted := client.Subscribe(codex.ThreadFilter{ThreadID: first})
	defer deleted.Close()
	remove := func(item nativeAttachment) {
		require.NoError(t, client.Call(ctx, "thread/attachment/remove", map[string]any{
			"threadId": first, "attachmentType": item.AttachmentType, "identityKey": item.IdentityKey}, nil))
		awaitNativeAttachmentNotice(t, ctx, deleted, first, item, "deleted")
	}
	remove(original.Attachment)
	require.ElementsMatch(t, []nativeAttachment{second.Attachment, differentType.Attachment}, nativeAttachments(t, ctx, client, first))
	require.Equal(t, []nativeAttachment{isolated.Attachment}, nativeAttachments(t, ctx, client, other), "删除不能跨会话影响同身份附件")
	remove(second.Attachment)
	remove(differentType.Attachment)
	require.NoError(t, registry.Restart(runtimeidentity.Codex))
	client = connectRuntimeSSH(t, ctx, connection, runtimeidentity.Codex)
	require.Empty(t, nativeAttachments(t, ctx, client, first))
	require.Equal(t, []nativeAttachment{isolated.Attachment}, nativeAttachments(t, ctx, client, other))
	for _, threadID := range []string{first, other} {
		require.Len(t, readSessionThread(t, ctx, client, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true}).Turns, 1)
	}
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "附件管理不能改写或删除源文件", string(data))
}
