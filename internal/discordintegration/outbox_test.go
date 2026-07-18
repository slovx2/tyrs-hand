package discordintegration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	disgorest "github.com/disgoorg/disgo/rest"
	"github.com/stretchr/testify/require"
)

type fakeOutboxStore struct {
	item      *OutboxItem
	completed bool
	failed    error
	retryAt   time.Time
	retryErr  error
}

func (s *fakeOutboxStore) Claim(context.Context, time.Duration) (*OutboxItem, error) {
	item := s.item
	s.item = nil
	return item, nil
}
func (s *fakeOutboxStore) Complete(context.Context, OutboxItem, json.RawMessage) error {
	s.completed = true
	return nil
}
func (s *fakeOutboxStore) Retry(_ context.Context, _ OutboxItem, at time.Time, err error) error {
	s.retryAt, s.retryErr = at, err
	return nil
}
func (s *fakeOutboxStore) Fail(_ context.Context, _ OutboxItem, err error) error {
	s.failed = err
	return nil
}

type fakeRemote struct {
	send func(OutboxItem) (json.RawMessage, error)
}

func (f *fakeRemote) Guild(context.Context, string) (RemoteGuild, error) { return RemoteGuild{}, nil }
func (f *fakeRemote) DisableCommunity(context.Context, string) error     { return nil }
func (f *fakeRemote) EnableCommunity(context.Context, string, string, string) error {
	return nil
}
func (f *fakeRemote) CreateChannel(context.Context, string, ChannelSpec, string) (RemoteChannel, error) {
	return RemoteChannel{}, nil
}
func (f *fakeRemote) UpdateChannel(context.Context, string, ChannelSpec) error { return nil }
func (f *fakeRemote) DeleteChannel(context.Context, string) error              { return nil }
func (f *fakeRemote) Send(_ context.Context, item OutboxItem) (json.RawMessage, error) {
	return f.send(item)
}
func (f *fakeRemote) Close(context.Context) {}

func TestDispatcherRetries429UsingServerDelay(t *testing.T) {
	header := make(http.Header)
	header.Set("Retry-After", "1.25")
	remoteErr := &disgorest.Error{Response: &http.Response{StatusCode: 429, Status: "429", Header: header}}
	store := &fakeOutboxStore{item: &OutboxItem{ID: "1", Attempt: 1, MaxAttempts: 3}}
	dispatcher := NewDispatcher(store, &fakeRemote{send: func(OutboxItem) (json.RawMessage, error) {
		return nil, remoteErr
	}})
	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	dispatcher.now = func() time.Time { return now }
	dispatcher.jitter = func(time.Duration) time.Duration { return 0 }

	worked, err := dispatcher.RunOnce(context.Background())
	require.NoError(t, err)
	require.True(t, worked)
	require.Equal(t, now.Add(1250*time.Millisecond), store.retryAt)
	require.Error(t, store.retryErr)
	require.NoError(t, store.failed)
}

func TestDispatcherClassifiesPermanentDiscordErrors(t *testing.T) {
	tests := []struct {
		status int
		target error
	}{{401, ErrUnauthorized}, {403, ErrPermission}, {404, ErrResourceGone}}
	for _, test := range tests {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			remoteErr := &disgorest.Error{Response: &http.Response{StatusCode: test.status, Status: http.StatusText(test.status), Header: make(http.Header)}}
			store := &fakeOutboxStore{item: &OutboxItem{ID: "1", Attempt: 1, MaxAttempts: 3}}
			dispatcher := NewDispatcher(store, &fakeRemote{send: func(OutboxItem) (json.RawMessage, error) {
				return nil, remoteErr
			}})
			worked, err := dispatcher.RunOnce(context.Background())
			if test.target == ErrUnauthorized {
				require.ErrorIs(t, err, ErrUnauthorized)
			} else {
				require.NoError(t, err)
			}
			require.True(t, worked)
			require.ErrorIs(t, store.failed, test.target)
			require.True(t, store.retryAt.IsZero())
		})
	}
}

func TestDispatcherStopsAfterThreeAmbiguousAttempts(t *testing.T) {
	store := &fakeOutboxStore{item: &OutboxItem{ID: "1", Attempt: 3, MaxAttempts: 3}}
	dispatcher := NewDispatcher(store, &fakeRemote{send: func(OutboxItem) (json.RawMessage, error) {
		return nil, ErrAmbiguousWrite
	}})
	worked, err := dispatcher.RunOnce(context.Background())
	require.NoError(t, err)
	require.True(t, worked)
	require.ErrorIs(t, store.failed, ErrAmbiguousWrite)
}

func TestDispatcherUsesBackoffForServerErrors(t *testing.T) {
	remoteErr := &disgorest.Error{Response: &http.Response{StatusCode: http.StatusBadGateway, Header: make(http.Header)}}
	store := &fakeOutboxStore{item: &OutboxItem{ID: "1", Attempt: 1, MaxAttempts: 3}}
	dispatcher := NewDispatcher(store, &fakeRemote{send: func(OutboxItem) (json.RawMessage, error) {
		return nil, remoteErr
	}})
	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	dispatcher.now = func() time.Time { return now }
	worked, err := dispatcher.RunOnce(context.Background())
	require.NoError(t, err)
	require.True(t, worked)
	require.GreaterOrEqual(t, store.retryAt, now.Add(time.Second))
	require.LessOrEqual(t, store.retryAt, now.Add(1500*time.Millisecond))
}

func TestDisgoRemoteHandlesRouteAndGlobalRateLimits(t *testing.T) {
	for _, global := range []bool{false, true} {
		t.Run(map[bool]string{false: "route", true: "global"}[global], func(t *testing.T) {
			var mu sync.Mutex
			calls := 0
			var sent map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				calls++
				require.Equal(t, "/channels/123/messages", request.URL.Path)
				require.NoError(t, json.NewDecoder(request.Body).Decode(&sent))
				w.Header().Set("Via", "discord")
				w.Header().Set("X-RateLimit-Bucket", "messages")
				w.Header().Set("X-RateLimit-Limit", "1")
				w.Header().Set("X-RateLimit-Reset-After", "0")
				if calls == 1 {
					w.Header().Set("X-RateLimit-Remaining", "0")
					w.Header().Set("Retry-After", "0")
					if global {
						w.Header().Set("X-RateLimit-Global", "true")
					}
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = w.Write([]byte(`{"message":"limited"}`))
					return
				}
				w.Header().Set("X-RateLimit-Remaining", "1")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"456","channel_id":"123","content":"hello"}`))
			}))
			defer server.Close()

			remote := NewDisgoRemote("token", server.URL, server.Client())
			defer remote.Close(context.Background())
			payload, _ := json.Marshal(map[string]string{"channelId": "123", "content": "hello"})
			response, err := remote.Send(context.Background(), OutboxItem{
				OperationType: "message.create", Payload: payload, Nonce: "stable-nonce",
			})
			require.NoError(t, err)
			require.JSONEq(t, `{"messageId":"456"}`, string(response))
			require.Equal(t, 2, calls)
			require.Equal(t, "stable-nonce", sent["nonce"])
			require.Equal(t, true, sent["enforce_nonce"])
		})
	}
}

func TestDisgoRemoteDefersInteractionEphemerally(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/interactions/777/secret/callback", request.URL.Path)
		require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	remote := NewDisgoRemote("token", server.URL, server.Client())
	defer remote.Close(context.Background())
	input, _ := json.Marshal(map[string]any{"interactionId": "777", "interactionToken": "secret", "ephemeral": true})
	_, err := remote.Send(context.Background(), OutboxItem{OperationType: "interaction.defer", Payload: input})
	require.NoError(t, err)
	require.Equal(t, float64(5), payload["type"])
	data, ok := payload["data"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(64), data["flags"])
}

var _ = errors.Is
