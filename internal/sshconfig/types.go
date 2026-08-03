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
	ID              uuid.UUID   `json:"id"`
	Alias           string      `json:"alias"`
	Hostname        string      `json:"hostname"`
	Port            int         `json:"port"`
	Username        string      `json:"username"`
	CredentialID    uuid.UUID   `json:"credentialId"`
	CredentialName  string      `json:"credentialName"`
	ProxyJumpHostID *uuid.UUID  `json:"proxyJumpHostId,omitempty"`
	ProxyJumpAlias  string      `json:"proxyJumpAlias,omitempty"`
	WorkerIDs       []uuid.UUID `json:"workerIds"`
	Enabled         bool        `json:"enabled"`
	UpdatedAt       time.Time   `json:"updatedAt"`
}

type HostInput struct {
	Alias           string      `json:"alias"`
	Hostname        string      `json:"hostname"`
	Port            int         `json:"port"`
	Username        string      `json:"username"`
	CredentialID    uuid.UUID   `json:"credentialId"`
	ProxyJumpHostID *uuid.UUID  `json:"proxyJumpHostId"`
	WorkerIDs       []uuid.UUID `json:"workerIds"`
	Enabled         *bool       `json:"enabled"`
}

type HostImportInput struct {
	CredentialID uuid.UUID        `json:"credentialId"`
	WorkerIDs    []uuid.UUID      `json:"workerIds"`
	Enabled      *bool            `json:"enabled"`
	Hosts        []HostImportItem `json:"hosts"`
}

type HostImportItem struct {
	Alias          string `json:"alias"`
	Hostname       string `json:"hostname"`
	Port           int    `json:"port"`
	Username       string `json:"username"`
	ProxyJumpAlias string `json:"proxyJumpAlias,omitempty"`
}

type WorkerCredential struct {
	ID          uuid.UUID `json:"id"`
	PrivateKey  string    `json:"privateKey"`
	Passphrase  string    `json:"passphrase,omitempty"`
	PublicKey   string    `json:"publicKey"`
	Fingerprint string    `json:"fingerprint"`
}

type WorkerHost struct {
	Alias          string    `json:"alias"`
	Hostname       string    `json:"hostname"`
	Port           int       `json:"port"`
	Username       string    `json:"username"`
	CredentialID   uuid.UUID `json:"credentialId"`
	ProxyJumpAlias string    `json:"proxyJumpAlias,omitempty"`
}

type WorkerConfiguration struct {
	Revision    string             `json:"revision"`
	Credentials []WorkerCredential `json:"credentials"`
	Hosts       []WorkerHost       `json:"hosts"`
}

type secretPayload struct {
	PrivateKey string `json:"privateKey"`
	Passphrase string `json:"passphrase,omitempty"`
}
