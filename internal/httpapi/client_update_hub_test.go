package httpapi

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestClientUpdateHubBroadcastsToConcurrentConnections(t *testing.T) {
	hub := newClientUpdateHub()
	first, cancelFirst := hub.subscribe()
	defer cancelFirst()
	second, cancelSecond := hub.subscribe()
	defer cancelSecond()
	update := clientUpdate{SessionID: pointer(uuid.New()), Type: "message.delta",
		EntityID: "run-1", CreatedAt: time.Now()}
	hub.publish(update)

	require.Equal(t, update, <-first)
	require.Equal(t, update, <-second)
}

func TestClientUpdateHubDisconnectsSlowConnection(t *testing.T) {
	hub := newClientUpdateHub()
	updates, cancel := hub.subscribe()
	defer cancel()
	for index := 0; index < 65; index++ {
		hub.publish(clientUpdate{Type: "message.delta", EntityID: "run-1"})
	}
	for range 64 {
		_, open := <-updates
		require.True(t, open)
	}
	_, open := <-updates
	require.False(t, open)
}

func pointer[T any](value T) *T { return &value }
