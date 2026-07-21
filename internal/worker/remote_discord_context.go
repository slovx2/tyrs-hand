package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/devcontainer"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

type remoteSavedAttachment struct {
	workerprotocol.Attachment
	RelativePath string
}

func (p *RemoteProcessor) prepareRemoteAttachments(ctx context.Context,
	task *workerprotocol.Task, runtime devcontainer.Runtime,
) ([]remoteSavedAttachment, error) {
	attachments := task.Snapshot.Discord.Attachments
	if len(attachments) == 0 {
		return nil, nil
	}
	temporary, err := os.MkdirTemp(filepath.Join(p.cfg.WorkerDataRoot, "tmp"),
		"discord-attachments-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	directory := filepath.Join(temporary, ".tyrs-hand", "discord-attachments")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	result := make([]remoteSavedAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		filename := filepath.Base(strings.TrimSpace(attachment.Filename))
		filename = strings.Trim(remoteAttachmentName.ReplaceAllString(filename, "_"), " .")
		if filename == "" || filename == "." || filename == ".." {
			return nil, errors.New("control 返回的附件文件名无效")
		}
		relative := filepath.ToSlash(filepath.Join(".tyrs-hand", "discord-attachments",
			attachment.ID.String()+"-"+filename))
		target := filepath.Join(temporary, filepath.FromSlash(relative))
		file, err := os.OpenFile(target+".tmp", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		hash := sha256.New()
		headerHash, size, downloadErr := p.client.DownloadAttachment(ctx, task, attachment.ID,
			io.MultiWriter(file, hash))
		if downloadErr == nil {
			downloadErr = file.Sync()
		}
		closeErr := file.Close()
		if downloadErr != nil {
			_ = os.Remove(target + ".tmp")
			return nil, downloadErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		actual := hex.EncodeToString(hash.Sum(nil))
		if size != attachment.Size || actual != attachment.SHA256 ||
			(headerHash != "" && headerHash != actual) {
			_ = os.Remove(target + ".tmp")
			return nil, fmt.Errorf("discord 附件 %s 的大小或 SHA-256 校验失败", attachment.ID)
		}
		if err := os.Rename(target+".tmp", target); err != nil {
			return nil, err
		}
		result = append(result, remoteSavedAttachment{Attachment: attachment,
			RelativePath: relative})
	}
	if err := p.development.CopyToRuntime(ctx, runtime, temporary, runtime.Workspace); err != nil {
		return nil, err
	}
	return result, nil
}

func remoteDiscordTurnInput(snapshot *workerprotocol.DiscordSnapshot,
	runtime devcontainer.Runtime, attachments []remoteSavedAttachment,
	skills []ports.SkillRef,
) ports.TurnInput {
	identity := discordintegration.MessageIdentity{
		GuildID: snapshot.GuildID, DiscordUserID: snapshot.UserID,
		GitHubUserID: snapshot.GitHubUserID, GitHubLogin: snapshot.GitHubLogin,
		BindingID: snapshot.BindingID, BindingVersion: snapshot.BindingVersion,
		Access: snapshot.Access, MessageID: snapshot.MessageID,
		DisplayName: snapshot.DisplayName, Username: snapshot.Username,
	}
	additional := discordintegration.AdditionalContext(identity)
	var images []ports.LocalImageInput
	var files []map[string]string
	for _, attachment := range attachments {
		path := filepath.ToSlash(filepath.Join(runtime.Workspace,
			filepath.FromSlash(attachment.RelativePath)))
		if attachment.Kind == "image" {
			images = append(images, ports.LocalImageInput{Path: path, Detail: "auto"})
		} else {
			files = append(files, map[string]string{"filename": attachment.Filename,
				"relative_path": attachment.RelativePath, "media_type": attachment.MediaType,
				"sha256": attachment.SHA256})
		}
	}
	if len(files) > 0 {
		encoded, _ := json.Marshal(map[string]any{"message_id": snapshot.MessageID,
			"files": files})
		additional["discord_message_attachments"] = ports.AdditionalContextEntry{
			Kind: "application", Value: string(encoded),
		}
	}
	return ports.TurnInput{Text: snapshot.Body, ClientUserMessageID: snapshot.MessageID,
		LocalImages: images, AdditionalContext: additional, Skills: skills}
}

func (p *RemoteProcessor) discordCommandHandler(primary *workerprotocol.Task,
	containerRuntime devcontainer.Runtime, skills []ports.SkillRef,
	report func(string, json.RawMessage),
) remoteCommandHandler {
	return func(ctx context.Context, runtime *codex.Runtime, threadID, turnID string,
		command workerprotocol.RunCommand,
	) error {
		if command.Discord == nil {
			return errors.New("discord steer 指令缺少消息快照")
		}
		commandTask := *primary
		commandTask.Claimed.ID = command.ID
		commandTask.Claimed.DiscordMessageID = command.Discord.MessageID
		commandTask.Snapshot.Discord = command.Discord
		attachments, err := p.prepareRemoteAttachments(ctx, &commandTask, containerRuntime)
		if err != nil {
			return err
		}
		input := remoteDiscordTurnInput(command.Discord, containerRuntime, attachments, skills)
		if err := runtime.SteerTurn(ctx, threadID, turnID, input); err != nil {
			return err
		}
		if err := p.client.AckCommand(ctx, primary, command, "steer", turnID); err != nil {
			return err
		}
		report("discord.progress", remoteEventPayload(map[string]string{
			"state": "running", "detail": "已将新消息合并到当前 Codex Turn。",
		}))
		return nil
	}
}
