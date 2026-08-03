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
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

type remoteSavedAttachment struct {
	workerprotocol.Attachment
	RelativePath string
}

type hostWorkspaceRuntime struct {
	Workspace   string
	CodexHome   string
	ProjectKind string
	RemoteURL   string
}

func (p *Processor) prepareRemoteAttachments(ctx context.Context,
	task *workerprotocol.Task, runtime hostWorkspaceRuntime,
) ([]remoteSavedAttachment, error) {
	var attachments []workerprotocol.Attachment
	if task.Snapshot.Session != nil {
		attachments = task.Snapshot.Session.Attachments
	}
	if task.Snapshot.Discord != nil && len(task.Snapshot.Discord.Attachments) > 0 {
		attachments = task.Snapshot.Discord.Attachments
	}
	if len(attachments) == 0 {
		return nil, nil
	}
	directory := filepath.Join(runtime.Workspace, ".tyrs-hand", "discord-attachments",
		task.Claimed.ID.String())
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
			task.Claimed.ID.String(),
			attachment.ID.String()+"-"+filename))
		target := filepath.Join(runtime.Workspace, filepath.FromSlash(relative))
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
	return result, nil
}

func remoteWorkspaceTurnInput(snapshot *workerprotocol.SessionSnapshot,
	discord *workerprotocol.DiscordSnapshot, runtime hostWorkspaceRuntime,
	attachments []remoteSavedAttachment, skills []ports.SkillRef,
) ports.TurnInput {
	if discord != nil {
		input := remoteDiscordTurnInput(discord, runtime, attachments, skills)
		input.Text = snapshot.Body
		input.ClientUserMessageID = snapshot.MessageID
		return input
	}
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
	additional := map[string]ports.AdditionalContextEntry{}
	if len(files) > 0 {
		encoded, _ := json.Marshal(map[string]any{"message_id": snapshot.MessageID,
			"files": files})
		additional["session_message_attachments"] = ports.AdditionalContextEntry{
			Kind: "application", Value: string(encoded)}
	}
	return ports.TurnInput{Text: snapshot.Body, ClientUserMessageID: snapshot.MessageID,
		LocalImages: images, AdditionalContext: additional, Skills: skills}
}

func remoteDiscordTurnInput(snapshot *workerprotocol.DiscordSnapshot,
	runtime hostWorkspaceRuntime, attachments []remoteSavedAttachment,
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

func (p *Processor) hostDiscordCommandHandler(primary *workerprotocol.Task,
	hostRuntime hostWorkspaceRuntime, skills []ports.SkillRef,
	report func(string, json.RawMessage),
) remoteCommandHandler {
	return func(ctx context.Context, runtime *codex.Runtime, threadID, turnID string,
		command workerprotocol.RunCommand,
	) error {
		if command.Session == nil {
			return errors.New("workspace steer 指令缺少消息快照")
		}
		commandTask := *primary
		commandTask.Claimed.ID = command.ID
		commandTask.Claimed.DiscordMessageID = ""
		if command.Discord != nil {
			commandTask.Claimed.DiscordMessageID = command.Discord.MessageID
		}
		commandTask.Snapshot.Session = command.Session
		commandTask.Snapshot.Discord = command.Discord
		attachments, err := p.prepareRemoteAttachments(ctx, &commandTask, hostRuntime)
		if err != nil {
			return err
		}
		input := remoteWorkspaceTurnInput(command.Session, command.Discord,
			hostRuntime, attachments, skills)
		if err := runtime.SteerTurn(ctx, threadID, turnID, input); err != nil {
			return err
		}
		if err := p.client.AckCommand(ctx, primary, command, "steer", turnID); err != nil {
			return err
		}
		return nil
	}
}
