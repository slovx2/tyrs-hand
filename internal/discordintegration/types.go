package discordintegration

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

const (
	AccessReadOnly = "readonly"
	AccessOperator = "operator"
	AccessOwner    = "owner"
)

var (
	ErrReadOnly       = errors.New("当前成员只有只读权限")
	ErrResourceGone   = errors.New("discord 资源已经失效")
	ErrPermission     = errors.New("discord Bot 权限不足")
	ErrUnauthorized   = errors.New("discord Bot Token 无效")
	ErrAmbiguousWrite = errors.New("discord 写请求结果不明确")
)

type Settings struct {
	GuildID         string `json:"guildId"`
	Enabled         bool   `json:"enabled"`
	Community       bool   `json:"communityEnabled"`
	ApplicationID   string `json:"applicationId,omitempty"`
	BotUserID       string `json:"botUserId,omitempty"`
	TokenConfigured bool   `json:"tokenConfigured"`
}

type SettingsInput struct {
	GuildID       string `json:"guildId"`
	Enabled       bool   `json:"enabled"`
	BotToken      string `json:"botToken"`
	ApplicationID string `json:"applicationId"`
	BotUserID     string `json:"botUserId"`
}

type Status struct {
	Configured       bool       `json:"configured"`
	Enabled          bool       `json:"enabled"`
	GatewayStatus    string     `json:"gatewayStatus"`
	GatewayError     string     `json:"gatewayError,omitempty"`
	LastGatewayAt    *time.Time `json:"lastGatewayAt,omitempty"`
	PendingOutbox    int64      `json:"pendingOutbox"`
	FailedOutbox     int64      `json:"failedOutbox"`
	PendingOperation int64      `json:"pendingInitializationOperations"`
}

type RemoteGuild struct {
	ID               string
	Name             string
	CommunityEnabled bool
	Channels         []RemoteChannel
}

type RemoteChannel struct {
	ID       string
	ParentID string
	Name     string
	Kind     string
	Topic    string
	Tags     map[string]string
}

type ChannelSpec struct {
	Key                  string
	ParentKey            string
	Name                 string
	Kind                 string
	Topic                string
	PermissionOverwrites []PermissionSpec
	Tags                 []string
}

type PermissionSpec struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Allow int64  `json:"allow"`
	Deny  int64  `json:"deny"`
}

type Remote interface {
	Guild(context.Context, string) (RemoteGuild, error)
	DisableCommunity(context.Context, string) error
	EnableCommunity(context.Context, string, string, string) error
	CreateChannel(context.Context, string, ChannelSpec, string) (RemoteChannel, error)
	UpdateChannel(context.Context, string, ChannelSpec) error
	DeleteChannel(context.Context, string) error
	Send(context.Context, OutboxItem) (json.RawMessage, error)
	Close(context.Context)
}

type ManagedResource struct {
	Key       string
	DiscordID string
	ParentID  string
	Name      string
	Kind      string
	Marker    string
}

type Conflict struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type PreflightResult struct {
	GuildID       string     `json:"guildId"`
	Mode          string     `json:"mode"`
	Creates       []string   `json:"creates"`
	Updates       []string   `json:"updates"`
	Deletes       []string   `json:"deletes"`
	Conflicts     []Conflict `json:"conflicts"`
	MissingAccess []string   `json:"missingPermissions"`
	Channels      int        `json:"channelCount"`
	Safe          bool       `json:"safe"`
}

type Operation struct {
	ID        string          `json:"id"`
	GuildID   string          `json:"guildId"`
	Mode      string          `json:"mode"`
	Status    string          `json:"status"`
	Preflight PreflightResult `json:"preflight"`
	Error     string          `json:"error,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

type Member struct {
	GuildID       string `json:"guildId"`
	DiscordUserID string `json:"discordUserId"`
	Username      string `json:"username"`
	DisplayName   string `json:"displayName"`
	Bound         bool   `json:"bound"`
	GitHubLogin   string `json:"githubLogin,omitempty"`
	ForumID       string `json:"forumId,omitempty"`
}

type ForumAccess struct {
	ForumID             string `json:"forumId"`
	MemberID            string `json:"memberId"`
	AccessLevel         string `json:"accessLevel"`
	AdministratorBypass bool   `json:"administratorBypass"`
}
