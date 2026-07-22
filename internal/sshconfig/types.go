package sshconfig

import (
	"time"

	"github.com/google/uuid"
)

type Credential struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	PublicKey   string    `json:"publicKey"`
	Fingerprint string    `json:"fingerprint"`
	Enabled     bool      `json:"enabled"`
	Version     int64     `json:"version"`
	HostCount   int       `json:"hostCount"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type CredentialInput struct {
	Name       string `json:"name"`
	PrivateKey string `json:"privateKey"`
	Passphrase string `json:"passphrase"`
	Enabled    *bool  `json:"enabled"`
}

type Host struct {
	ID               uuid.UUID   `json:"id"`
	Alias            string      `json:"alias"`
	Hostname         string      `json:"hostname"`
	Port             int         `json:"port"`
	Username         string      `json:"username"`
	CredentialID     uuid.UUID   `json:"credentialId"`
	CredentialName   string      `json:"credentialName"`
	ProxyJumpHostID  *uuid.UUID  `json:"proxyJumpHostId,omitempty"`
	ProxyJumpAlias   string      `json:"proxyJumpAlias,omitempty"`
	ExecutionNodeIDs []uuid.UUID `json:"executionNodeIds"`
	Enabled          bool        `json:"enabled"`
	UpdatedAt        time.Time   `json:"updatedAt"`
}

type HostInput struct {
	Alias            string      `json:"alias"`
	Hostname         string      `json:"hostname"`
	Port             int         `json:"port"`
	Username         string      `json:"username"`
	CredentialID     uuid.UUID   `json:"credentialId"`
	ProxyJumpHostID  *uuid.UUID  `json:"proxyJumpHostId"`
	ExecutionNodeIDs []uuid.UUID `json:"executionNodeIds"`
	Enabled          *bool       `json:"enabled"`
}

type NodeCredential struct {
	ID          uuid.UUID `json:"id"`
	PrivateKey  string    `json:"privateKey"`
	Passphrase  string    `json:"passphrase,omitempty"`
	PublicKey   string    `json:"publicKey"`
	Fingerprint string    `json:"fingerprint"`
}

type NodeHost struct {
	Alias          string    `json:"alias"`
	Hostname       string    `json:"hostname"`
	Port           int       `json:"port"`
	Username       string    `json:"username"`
	CredentialID   uuid.UUID `json:"credentialId"`
	ProxyJumpAlias string    `json:"proxyJumpAlias,omitempty"`
}

type NodeConfiguration struct {
	Revision    string           `json:"revision"`
	Credentials []NodeCredential `json:"credentials"`
	Hosts       []NodeHost       `json:"hosts"`
}

type secretPayload struct {
	PrivateKey string `json:"privateKey"`
	Passphrase string `json:"passphrase,omitempty"`
}
