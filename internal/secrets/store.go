package secrets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/slovx2/tyrs-hand/internal/security"
)

type Store struct {
	db  *sql.DB
	box *security.SecretBox
}

type executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func NewStore(db *sql.DB, box *security.SecretBox) *Store {
	return &Store{db: db, box: box}
}

func (s *Store) Put(ctx context.Context, key string, value []byte) error {
	return s.put(ctx, s.db, key, value)
}

func (s *Store) PutTx(ctx context.Context, tx *sql.Tx, key string, value []byte) error {
	return s.put(ctx, tx, key, value)
}

func (s *Store) put(ctx context.Context, exec executor, key string, value []byte) error {
	nonce, ciphertext, err := s.box.Encrypt(value, key)
	if err != nil {
		return err
	}
	_, err = exec.ExecContext(ctx, `
		INSERT INTO encrypted_secrets(secret_key, nonce, ciphertext)
		VALUES ($1, $2, $3)
		ON CONFLICT (secret_key) DO UPDATE
		SET nonce = EXCLUDED.nonce, ciphertext = EXCLUDED.ciphertext,
			key_version = encrypted_secrets.key_version + 1, updated_at = now()`, key, nonce, ciphertext)
	return err
}

func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	var nonce, ciphertext []byte
	err := s.db.QueryRowContext(ctx, "SELECT nonce, ciphertext FROM encrypted_secrets WHERE secret_key = $1", key).Scan(&nonce, &ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("加密 Secret %s 未配置", key)
	}
	if err != nil {
		return nil, err
	}
	return s.box.Decrypt(nonce, ciphertext, key)
}
