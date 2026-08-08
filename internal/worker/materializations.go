package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

type materializationTarget interface {
	MaterializationCacheDir() (string, error)
}

func (p *Processor) MaterializationCacheDir() (string, error) {
	if p.hostRuntime == nil {
		return "", errors.New("宿主 Codex Runtime 尚未启动")
	}
	return filepath.Join(p.hostRuntime.StateDir(), "materializations"), nil
}

func (r *Runner) materializationLoop(ctx context.Context, target materializationTarget) {
	for ctx.Err() == nil {
		claimCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		claim, err := r.client.ClaimMaterialization(claimCtx)
		cancel()
		if err != nil {
			r.logger.Warn("领取附件 materialization 失败", zap.Error(err))
			if !waitContext(ctx, time.Second) {
				return
			}
			continue
		}
		if claim.Materialization == nil {
			if !waitContext(ctx, 300*time.Millisecond) {
				return
			}
			continue
		}
		r.processMaterialization(ctx, target, claim.Materialization)
	}
}

func (r *Runner) processMaterialization(ctx context.Context, target materializationTarget,
	task *workerprotocol.MaterializationClaim,
) {
	deadline := task.ExpiresAt
	if deadline.IsZero() {
		deadline = time.Now().Add(2 * time.Minute)
	}
	materializeCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	remotePath, err := r.materializeAttachment(materializeCtx, target, task)
	if err == nil {
		err = r.client.CompleteMaterialization(materializeCtx, task.ID,
			workerprotocol.MaterializationCompleteRequest{
				LeaseToken: task.LeaseToken, RemotePath: remotePath,
			})
	}
	if err == nil {
		return
	}
	message := err.Error()
	if len(message) > 2000 {
		message = message[:2000]
	}
	failCtx, failCancel := context.WithTimeout(ctx, 10*time.Second)
	defer failCancel()
	if failErr := r.client.FailMaterialization(failCtx, task.ID,
		workerprotocol.MaterializationFailRequest{
			LeaseToken: task.LeaseToken, Error: message,
		}); failErr != nil && ctx.Err() == nil {
		r.logger.Warn("回报附件 materialization 失败", zap.Error(failErr),
			zap.String("materialization_id", task.ID.String()))
	}
}

func (r *Runner) materializeAttachment(ctx context.Context, target materializationTarget,
	task *workerprotocol.MaterializationClaim,
) (string, error) {
	digest := strings.ToLower(strings.TrimSpace(task.SHA256))
	if len(digest) != sha256.Size*2 || task.SizeBytes <= 0 || task.SizeBytes > 25<<20 {
		return "", errors.New("materialization 元数据无效")
	}
	directory, err := target.MaterializationCacheDir()
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("创建 materialization 缓存: %w", err)
	}
	if err = os.Chmod(directory, 0o700); err != nil {
		return "", fmt.Errorf("保护 materialization 缓存: %w", err)
	}
	finalPath := filepath.Join(directory, digest)
	if validMaterializedFile(finalPath, digest, task.SizeBytes) {
		return finalPath, nil
	}
	temporary, err := os.CreateTemp(directory, ".materialization-*")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err = temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return "", err
	}
	hash := sha256.New()
	headerDigest, written, downloadErr := r.client.DownloadMaterialization(ctx, task,
		io.MultiWriter(temporary, hash))
	if syncErr := temporary.Sync(); downloadErr == nil {
		downloadErr = syncErr
	}
	if closeErr := temporary.Close(); downloadErr == nil {
		downloadErr = closeErr
	}
	computed := hex.EncodeToString(hash.Sum(nil))
	if downloadErr != nil {
		return "", downloadErr
	}
	if written != task.SizeBytes || computed != digest ||
		!strings.EqualFold(strings.TrimSpace(headerDigest), digest) {
		return "", errors.New("materialization 内容长度或 SHA-256 不匹配")
	}
	if err = os.Rename(temporaryPath, finalPath); err != nil {
		return "", err
	}
	if err = syncDirectory(directory); err != nil {
		return "", err
	}
	return finalPath, nil
}

func validMaterializedFile(path, expectedDigest string, expectedSize int64) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != expectedSize {
		return false
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return false
	}
	return hex.EncodeToString(hash.Sum(nil)) == expectedDigest
}
