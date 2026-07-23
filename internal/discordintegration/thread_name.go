package discordintegration

import (
	"context"

	"github.com/google/uuid"
)

func EnqueueThreadName(ctx context.Context, execer discordOutboxExecer, controlID uuid.UUID,
	threadID, name string, revision int64,
) error {
	return enqueueDiscordOutbox(ctx, execer, "thread-name:"+controlID.String(),
		"thread.rename", "channels/"+threadID, map[string]any{
			"channelId": threadID, "threadName": name, "controlId": controlID.String(),
			"revision": revision,
		}, "")
}
