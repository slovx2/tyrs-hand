package auth

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
	"github.com/slovx2/tyrs-hand/internal/security"
)

var (
	ErrSetupComplete      = errors.New("系统初始化已经完成")
	ErrInvalidSetupToken  = errors.New("提供的 Setup Token 无效")
	ErrInvalidCredentials = errors.New("用户名、密码或 TOTP 无效")
	ErrSessionInvalid     = errors.New("登录会话无效")
)

const administratorSessionLifetime = 90 * 24 * time.Hour

type Service struct {
	db          *sql.DB
	box         *security.SecretBox
	setupToken  string
	publicURL   string
	sessionLife time.Duration
}

type SetupResult struct {
	TOTPSecret      string   `json:"totpSecret"`
	ProvisioningURI string   `json:"provisioningUri"`
	RecoveryCodes   []string `json:"recoveryCodes"`
}

type Session struct {
	AdministratorID uuid.UUID
	Username        string
	Token           string
	CSRFToken       string
	ExpiresAt       time.Time
}

func NewService(db *sql.DB, box *security.SecretBox, setupToken, publicURL string) *Service {
	return &Service{db: db, box: box, setupToken: setupToken, publicURL: publicURL, sessionLife: administratorSessionLifetime}
}

func (s *Service) SetupRequired(ctx context.Context) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM administrators)").Scan(&exists)
	return !exists, err
}

func (s *Service) Setup(ctx context.Context, setupToken, username, password string) (SetupResult, error) {
	required, err := s.SetupRequired(ctx)
	if err != nil {
		return SetupResult{}, err
	}
	if !required {
		return SetupResult{}, ErrSetupComplete
	}
	if !constantEqual(s.setupToken, setupToken) {
		return SetupResult{}, ErrInvalidSetupToken
	}
	passwordHash, err := security.HashPassword(password)
	if err != nil {
		return SetupResult{}, err
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "tyrs-hand", AccountName: username})
	if err != nil {
		return SetupResult{}, err
	}
	nonce, ciphertext, err := s.box.Encrypt([]byte(key.Secret()), "administrator.totp")
	if err != nil {
		return SetupResult{}, err
	}
	encrypted := append(nonce, ciphertext...)
	recoveryCodes, recoveryHashes, err := generateRecoveryCodes(10)
	if err != nil {
		return SetupResult{}, err
	}
	hashesJSON, err := json.Marshal(recoveryHashes)
	if err != nil {
		return SetupResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return SetupResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var id uuid.UUID
	err = tx.QueryRowContext(ctx, `
		INSERT INTO administrators(username, password_hash, totp_secret_ciphertext, recovery_codes_hash)
		SELECT $1, $2, $3, $4 WHERE NOT EXISTS (SELECT 1 FROM administrators)
		RETURNING id`, username, passwordHash, encrypted, hashesJSON).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return SetupResult{}, ErrSetupComplete
	}
	if err != nil {
		return SetupResult{}, fmt.Errorf("创建管理员: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_logs(administrator_id, action, resource_type, resource_id) VALUES ($1, 'setup.complete', 'administrator', $2)`, id, id.String()); err != nil {
		return SetupResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return SetupResult{}, err
	}
	return SetupResult{TOTPSecret: key.Secret(), ProvisioningURI: key.URL(), RecoveryCodes: recoveryCodes}, nil
}

func (s *Service) Login(ctx context.Context, username, password, code string) (Session, error) {
	var id uuid.UUID
	var storedUsername, passwordHash string
	var encrypted []byte
	err := s.db.QueryRowContext(ctx, `SELECT id, username, password_hash, totp_secret_ciphertext FROM administrators WHERE username = $1`, username).
		Scan(&id, &storedUsername, &passwordHash, &encrypted)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !security.VerifyPassword(passwordHash, password) {
		return Session{}, ErrInvalidCredentials
	}
	if err != nil {
		return Session{}, err
	}
	nonceSize := 12
	if len(encrypted) <= nonceSize {
		return Session{}, errors.New("管理员 TOTP 密文损坏")
	}
	secret, err := s.box.Decrypt(encrypted[:nonceSize], encrypted[nonceSize:], "administrator.totp")
	if err != nil || !totp.Validate(code, string(secret)) {
		return Session{}, ErrInvalidCredentials
	}
	token, err := security.RandomToken(32)
	if err != nil {
		return Session{}, err
	}
	csrf, err := security.RandomToken(24)
	if err != nil {
		return Session{}, err
	}
	expiresAt := time.Now().UTC().Add(s.sessionLife)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO admin_sessions(administrator_id, token_hash, csrf_token_hash, expires_at)
		VALUES ($1, $2, $3, $4)`, id, security.Digest(token), security.Digest(csrf), expiresAt)
	if err != nil {
		return Session{}, err
	}
	return Session{AdministratorID: id, Username: storedUsername, Token: token, CSRFToken: csrf, ExpiresAt: expiresAt}, nil
}

func (s *Service) Authenticate(ctx context.Context, token string) (Session, error) {
	if token == "" {
		return Session{}, ErrSessionInvalid
	}
	var session Session
	err := s.db.QueryRowContext(ctx, `
		UPDATE admin_sessions s SET last_seen_at = now()
		FROM administrators a
		WHERE s.administrator_id = a.id AND s.token_hash = $1 AND s.expires_at > now()
		RETURNING a.id, a.username, s.expires_at`, security.Digest(token)).
		Scan(&session.AdministratorID, &session.Username, &session.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrSessionInvalid
	}
	session.Token = token
	return session, err
}

func (s *Service) ValidateCSRF(ctx context.Context, token, csrf string) bool {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM admin_sessions WHERE token_hash = $1 AND csrf_token_hash = $2 AND expires_at > now()
	)`, security.Digest(token), security.Digest(csrf)).Scan(&exists)
	return err == nil && exists
}

func (s *Service) Logout(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM admin_sessions WHERE token_hash = $1", security.Digest(token))
	return err
}

func generateRecoveryCodes(count int) ([]string, []string, error) {
	codes := make([]string, 0, count)
	hashes := make([]string, 0, count)
	for range count {
		code, err := security.RandomToken(9)
		if err != nil {
			return nil, nil, err
		}
		codes = append(codes, code)
		hashes = append(hashes, security.Digest(code))
	}
	return codes, hashes, nil
}

func constantEqual(left, right string) bool {
	if len(left) == 0 || len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
