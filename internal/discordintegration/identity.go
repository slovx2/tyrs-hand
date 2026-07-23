package discordintegration

import (
	"github.com/slovx2/tyrs-hand/internal/participantidentity"
	"github.com/slovx2/tyrs-hand/internal/ports"
)

const (
	IdentityContextKey = participantidentity.IdentityContextKey
	ProfileContextKey  = participantidentity.ProfileContextKey
)

type MessageIdentity struct {
	GuildID        string
	DiscordUserID  string
	GitHubUserID   int64
	GitHubLogin    string
	BindingID      string
	BindingVersion int64
	Access         string
	MessageID      string
	DisplayName    string
	Username       string
}

func AdditionalContext(identity MessageIdentity) map[string]ports.AdditionalContextEntry {
	return participantidentity.AdditionalContext(participantidentity.Participant{
		ID:          participantidentity.ID(identity.GuildID, identity.DiscordUserID),
		DisplayName: identity.DisplayName,
	})
}

const MultiplayerDeveloperInstructions = participantidentity.DeveloperInstructions
