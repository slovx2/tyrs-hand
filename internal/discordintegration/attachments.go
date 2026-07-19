package discordintegration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const (
	DefaultMaxAttachments = 10
	DefaultMaxFileBytes   = int64(25 << 20)
)

var safeFilename = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

type AttachmentInput struct {
	ID        string
	URL       string
	Filename  string
	MediaType string
	Size      int64
}

type SavedAttachment struct {
	ID           string
	Kind         string
	Filename     string
	MediaType    string
	Size         int64
	SHA256       string
	RelativePath string
}

type AttachmentDownloader struct {
	client       *http.Client
	maxFiles     int
	maxFileBytes int64
	allowedHosts []string
}

func NewAttachmentDownloader(transport http.RoundTripper) *AttachmentDownloader {
	if transport == nil {
		transport = http.DefaultTransport
	}
	d := &AttachmentDownloader{
		maxFiles: DefaultMaxAttachments, maxFileBytes: DefaultMaxFileBytes,
		allowedHosts: []string{"cdn.discordapp.com", "media.discordapp.net"},
	}
	d.client = &http.Client{Transport: transport, CheckRedirect: d.checkRedirect}
	return d
}

func (d *AttachmentDownloader) Download(ctx context.Context, workspace string, inputs []AttachmentInput) ([]SavedAttachment, error) {
	if len(inputs) > d.maxFiles {
		return nil, fmt.Errorf("discord 附件不能超过 %d 个", d.maxFiles)
	}
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return nil, fmt.Errorf("解析会话工作区: %w", err)
	}
	directory := filepath.Join(root, ".tyrs-hand", "discord-attachments")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	resolvedDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil || !within(root, resolvedDirectory) {
		return nil, errors.New("附件目录不在会话工作区内")
	}
	result := make([]SavedAttachment, 0, len(inputs))
	for _, input := range inputs {
		saved, err := d.downloadOne(ctx, root, resolvedDirectory, input)
		if err != nil {
			return nil, err
		}
		result = append(result, saved)
	}
	return result, nil
}

func (d *AttachmentDownloader) downloadOne(ctx context.Context, root, directory string, input AttachmentInput) (SavedAttachment, error) {
	parsed, err := d.validateURL(input.URL)
	if err != nil {
		return SavedAttachment{}, err
	}
	if input.Size < 0 || input.Size > d.maxFileBytes {
		return SavedAttachment{}, errors.New("discord 附件大小超出限制")
	}
	filename, err := sanitizeFilename(input.Filename)
	if err != nil {
		return SavedAttachment{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return SavedAttachment{}, err
	}
	response, err := d.client.Do(request)
	if err != nil {
		return SavedAttachment{}, fmt.Errorf("下载 Discord 附件: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return SavedAttachment{}, fmt.Errorf("discord CDN 返回 %s", response.Status)
	}
	declared := normalizeMediaType(input.MediaType)
	responseType := normalizeMediaType(response.Header.Get("Content-Type"))
	extension := strings.ToLower(filepath.Ext(filename))
	// Gateway 元数据可能描述 Discord 代理后的格式，而附件 URL 返回原文件。
	// 下载响应是本次实际保存的字节，因此优先用它校验扩展名和类型。
	mediaType := responseType
	if mediaType == "" || mediaType == "application/octet-stream" {
		mediaType = declared
	}
	kind, err := validateAttachmentType(extension, mediaType)
	if err != nil {
		return SavedAttachment{}, err
	}
	localName := input.ID + "-" + filename
	if !validLocalComponent(input.ID) {
		return SavedAttachment{}, errors.New("discord Attachment ID 无效")
	}
	target := filepath.Join(directory, localName)
	if !within(root, target) {
		return SavedAttachment{}, errors.New("附件路径越过会话工作区")
	}
	temporary, err := os.OpenFile(target+".tmp", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return SavedAttachment{}, err
	}
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporary.Name())
		}
	}()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(response.Body, d.maxFileBytes+1))
	if err != nil {
		return SavedAttachment{}, err
	}
	if written > d.maxFileBytes {
		return SavedAttachment{}, errors.New("discord 附件实际大小超出限制")
	}
	if err := temporary.Sync(); err != nil {
		return SavedAttachment{}, err
	}
	if err := temporary.Close(); err != nil {
		return SavedAttachment{}, err
	}
	if err := os.Rename(temporary.Name(), target); err != nil {
		return SavedAttachment{}, err
	}
	keep = true
	relative, err := filepath.Rel(root, target)
	if err != nil || strings.HasPrefix(relative, "..") {
		_ = os.Remove(target)
		return SavedAttachment{}, errors.New("无法生成安全的附件相对路径")
	}
	return SavedAttachment{ID: input.ID, Kind: kind, Filename: filename, MediaType: mediaType,
		Size: written, SHA256: hex.EncodeToString(hash.Sum(nil)), RelativePath: filepath.ToSlash(relative)}, nil
}

func (d *AttachmentDownloader) checkRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= 3 {
		return errors.New("discord CDN 重定向次数过多")
	}
	_, err := d.validateURL(request.URL.String())
	return err
}

func (d *AttachmentDownloader) validateURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" {
		return nil, errors.New("discord 附件 URL 必须是无凭据、无自定义端口的 HTTPS URL")
	}
	if !slices.Contains(d.allowedHosts, strings.ToLower(parsed.Hostname())) {
		return nil, errors.New("discord 附件 URL 不属于允许的 CDN 域名")
	}
	return parsed, nil
}

func sanitizeFilename(value string) (string, error) {
	value = filepath.Base(strings.TrimSpace(value))
	value = safeFilename.ReplaceAllString(value, "_")
	value = strings.Trim(value, " .")
	if value == "" || value == "." || value == ".." || len(value) > 180 {
		return "", errors.New("discord 附件文件名无效")
	}
	return value, nil
}

func validateAttachmentType(extension, mediaType string) (string, error) {
	images := map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".webp": "image/webp"}
	if expected, ok := images[extension]; ok && mediaType == expected {
		return "image", nil
	}
	files := []string{".txt", ".md", ".log", ".json", ".yaml", ".yml", ".toml", ".xml", ".csv", ".go", ".py", ".js", ".ts", ".tsx", ".jsx", ".java", ".rs", ".c", ".h", ".cpp", ".sh", ".sql", ".diff", ".patch", ".pdf"}
	if slices.Contains(files, extension) {
		if extension == ".pdf" && mediaType != "application/pdf" {
			return "", errors.New("pdf 附件的 MIME 类型无效")
		}
		if extension != ".pdf" && mediaType != "" && !strings.HasPrefix(mediaType, "text/") && mediaType != "application/json" && mediaType != "application/octet-stream" {
			return "", errors.New("文本或源码附件的 MIME 类型无效")
		}
		return "file", nil
	}
	return "", errors.New("discord 附件类型不受支持")
}

func normalizeMediaType(value string) string {
	parsed, _, err := mime.ParseMediaType(value)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed)
}

func validLocalComponent(value string) bool {
	return value != "" && filepath.Base(value) == value && safeFilename.ReplaceAllString(value, "_") == value
}

func within(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
