package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

const (
	agentImageFileLimit  = 25 << 20
	agentImageCountLimit = 10
)

var markdownImagePattern = regexp.MustCompile(`!\[([^\]]*)\]\(([^\n)]+)\)`)

type agentImageCandidate struct {
	itemID string
	path   string
}

func (p *Processor) attachAgentImages(ctx context.Context, task *workerprotocol.Task,
	runtime *codex.Runtime, threadID, workspace string, result codexcontrol.TurnResult,
) (codexcontrol.TurnResult, error) {
	if task.Claimed.SourceType != codexcontrol.SourceWorkspace {
		if result.FinalAnswer == "" {
			return result, errors.New("codex turn 已完成但没有最终回复")
		}
		return result, nil
	}
	snapshot, err := runtime.ReadThread(ctx, threadID)
	if err != nil {
		return result, fmt.Errorf("读取 agent 图片快照: %w", err)
	}
	turn, found := snapshot.TurnByID(result.TurnID)
	if !found {
		return result, errors.New("agent 图片所属 Turn 在快照中不存在")
	}
	candidates := make([]agentImageCandidate, 0)
	for _, item := range turn.Items {
		if item.Type != "imageGeneration" || strings.EqualFold(item.Status, "failed") {
			continue
		}
		if strings.TrimSpace(item.SavedPath) == "" {
			return result, fmt.Errorf("imageGeneration %s 没有 savedPath", item.ID)
		}
		candidates = append(candidates, agentImageCandidate{itemID: item.ID,
			path: item.SavedPath})
	}
	cleaned, markdownCandidates := localMarkdownImages(result.FinalAnswer, workspace)
	result.FinalAnswer = strings.TrimSpace(cleaned)
	candidates = append(candidates, markdownCandidates...)
	seen := make(map[string]bool)
	for _, candidate := range candidates {
		if len(result.AttachmentIDs) >= agentImageCountLimit {
			return result, errors.New("agent 图片超过 10 张")
		}
		path, digest, err := validateAgentImage(candidate.path)
		if err != nil {
			return result, fmt.Errorf("agent 图片 %s 无效: %w", filepath.Base(candidate.path), err)
		}
		if seen[digest] {
			continue
		}
		seen[digest] = true
		itemID := strings.TrimSpace(candidate.itemID)
		if itemID == "" {
			itemID = "image-" + digest[:16]
		}
		if len(itemID) > 200 {
			itemID = itemID[:200]
		}
		uploaded, uploadErr := p.uploadAgentAttachment(ctx, task, itemID,
			len(result.AttachmentIDs), path)
		if uploadErr != nil {
			return result, fmt.Errorf("上传 agent 图片 %s: %w", filepath.Base(path), uploadErr)
		}
		if uploaded.AttachmentID == uuid.Nil {
			return result, errors.New("Control 未返回 agent 附件 ID")
		}
		result.AttachmentIDs = append(result.AttachmentIDs, uploaded.AttachmentID)
	}
	if result.FinalAnswer == "" && len(result.AttachmentIDs) == 0 {
		return result, errors.New("codex turn 已完成但没有最终回复或图片")
	}
	return result, nil
}

func (p *Processor) uploadAgentAttachment(ctx context.Context, task *workerprotocol.Task,
	itemID string, ordinal int, path string,
) (workerprotocol.AgentAttachmentUploadResult, error) {
	var result workerprotocol.AgentAttachmentUploadResult
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		requestCtx, cancel := context.WithTimeout(ctx, p.cfg.ControlTimeout)
		result, err = p.client.UploadAgentAttachment(requestCtx, task, itemID, ordinal, path)
		cancel()
		if err == nil || !retryableControlError(err) || attempt == 3 {
			return result, err
		}
		if !waitContext(ctx, time.Duration(attempt+1)*500*time.Millisecond) {
			return result, ctx.Err()
		}
	}
	return result, err
}

func localMarkdownImages(markdown, workspace string) (string, []agentImageCandidate) {
	candidates := make([]agentImageCandidate, 0)
	cleaned := markdownImagePattern.ReplaceAllStringFunc(markdown, func(match string) string {
		parts := markdownImagePattern.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		destination := strings.TrimSpace(parts[2])
		if strings.HasPrefix(destination, "<") && strings.HasSuffix(destination, ">") {
			destination = strings.TrimSpace(destination[1 : len(destination)-1])
		}
		resolved := destination
		parsed, err := url.Parse(destination)
		if err == nil && parsed.Scheme != "" {
			if parsed.Scheme != "file" {
				return match
			}
			resolved, err = url.PathUnescape(parsed.Path)
			if err != nil {
				return match
			}
		} else if !filepath.IsAbs(resolved) {
			if workspace == "" {
				return match
			}
			resolved = filepath.Join(workspace, filepath.FromSlash(resolved))
		}
		info, statErr := os.Stat(resolved)
		if statErr != nil || !info.Mode().IsRegular() {
			return ""
		}
		candidates = append(candidates, agentImageCandidate{path: resolved})
		return ""
	})
	return cleaned, candidates
}

func validateAgentImage(value string) (string, string, error) {
	path, err := filepath.Abs(filepath.Clean(value))
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", "", err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > agentImageFileLimit {
		return "", "", errors.New("文件为空、不是普通文件或超过 25 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = file.Close() }()
	probe := make([]byte, 512)
	read, readErr := file.Read(probe)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return "", "", readErr
	}
	mediaType := http.DetectContentType(probe[:read])
	if mediaType != "image/png" && mediaType != "image/jpeg" &&
		mediaType != "image/gif" && mediaType != "image/webp" {
		return "", "", errors.New("只支持 PNG、JPEG、GIF、WebP")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return "", "", err
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return "", "", err
	}
	return path, hex.EncodeToString(hash.Sum(nil)), nil
}
