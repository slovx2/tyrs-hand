package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

type workerIdentityCache struct {
	WorkerID         uuid.UUID `json:"workerId"`
	ControlURL       string    `json:"controlUrl"`
	CredentialDigest string    `json:"credentialDigest"`
}

func (r *Runner) WorkerID() string { return r.workerID.String() }

func (r *Runner) identityPath() string {
	return filepath.Join(r.cfg.WorkerDataRoot, "control-state", "identity.json")
}

func credentialDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func (r *Runner) cachedIdentity(credential string) (workerIdentityCache, error) {
	var cached workerIdentityCache
	data, err := os.ReadFile(r.identityPath())
	if err != nil {
		return cached, err
	}
	if err := json.Unmarshal(data, &cached); err != nil {
		return cached, err
	}
	if cached.WorkerID == uuid.Nil || cached.ControlURL != strings.TrimRight(r.cfg.WorkerControlURL, "/") ||
		cached.CredentialDigest != credentialDigest(credential) {
		return cached, errors.New("Worker 身份缓存与当前 Control 或凭据不符")
	}
	return cached, nil
}

func (r *Runner) saveIdentity(id uuid.UUID, credential string) error {
	if id == uuid.Nil {
		return errors.New("Worker UUID 不能为空")
	}
	data, err := json.Marshal(workerIdentityCache{WorkerID: id,
		ControlURL: strings.TrimRight(r.cfg.WorkerControlURL, "/"), CredentialDigest: credentialDigest(credential)})
	if err != nil {
		return err
	}
	// 使用与凭据相同的原子写入和 fsync，不缓存模型凭据。
	if err := writeCredential(r.identityPath(), string(data)); err != nil {
		return err
	}
	r.workerID = id
	return nil
}

func (r *Runner) resolveAuthenticatedIdentity(ctx context.Context, credential string) error {
	if cached, err := r.cachedIdentity(credential); err == nil {
		r.workerID = cached.WorkerID
		return nil
	}
	identity, err := r.client.Identity(ctx)
	if err != nil {
		return fmt.Errorf("查询已注册 Worker 身份: %w", err)
	}
	return r.saveIdentity(identity.WorkerID, credential)
}

// 离线启动沿用已确认身份；从未注册的本地 Worker 使用一次生成的 UUID。
func (r *Runner) InitializeOfflineIdentity() error {
	credential, err := readCredential(r.cfg.WorkerCredentialFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if cached, err := r.cachedIdentity(credential); err == nil {
		r.workerID = cached.WorkerID
		return nil
	}
	if credential != "" {
		return errors.New("离线 Worker 缺少与当前凭据匹配的身份缓存，需要先联网确认注册 UUID")
	}
	if _, err := os.Stat(r.identityPath()); !errors.Is(err, os.ErrNotExist) {
		return errors.New("本地 Worker 身份缓存无效，不能自动更换机器身份")
	}
	return r.saveIdentity(uuid.New(), "")
}
