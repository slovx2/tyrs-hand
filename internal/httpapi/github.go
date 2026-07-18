package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/auth"
	ghadapter "github.com/slovx2/tyrs-hand/internal/github"
	"github.com/slovx2/tyrs-hand/internal/orchestrator"
	"github.com/slovx2/tyrs-hand/internal/security"
	toolservice "github.com/slovx2/tyrs-hand/internal/tools"
)

func (s *Server) getGitHubApp(c *gin.Context) {
	settings, _, _, ok := s.github.Current()
	if !ok {
		c.JSON(http.StatusOK, gin.H{"configured": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{"configured": true, "appId": settings.AppID, "clientId": settings.ClientID, "appSlug": settings.AppSlug})
}

func (s *Server) putGitHubApp(c *gin.Context) {
	var settings ghadapter.AppSettings
	if err := c.ShouldBindJSON(&settings); err != nil {
		badRequest(c, err)
		return
	}
	if err := s.github.Save(c.Request.Context(), settings); err != nil {
		badRequest(c, err)
		return
	}
	s.audit(c, "github_app.update", "github_app", fmt.Sprint(settings.AppID), nil)
	c.Status(http.StatusNoContent)
}

func (s *Server) githubManifest(c *gin.Context) {
	state, err := security.RandomToken(24)
	if err != nil {
		problem(c, http.StatusInternalServerError, "创建 Manifest State 失败", err)
		return
	}
	session := c.MustGet("session").(auth.Session)
	if err := s.redis.Set(c.Request.Context(), "tyrs-hand:github-manifest:"+state, session.AdministratorID.String(), 10*time.Minute).Err(); err != nil {
		problem(c, http.StatusServiceUnavailable, "保存 Manifest State 失败", err)
		return
	}
	manifest := githubAppManifest(s.cfg.PublicURL, s.cfg.GitHubAppName)
	data, _ := json.Marshal(manifest)
	c.JSON(http.StatusOK, gin.H{"url": "https://github.com/settings/apps/new?state=" + url.QueryEscape(state), "manifest": string(data)})
}

func githubAppManifest(publicURL, appName string) map[string]any {
	return map[string]any{
		"name": appName, "url": publicURL,
		"hook_attributes": map[string]any{"url": publicURL + "/webhooks/github", "active": true},
		"redirect_url":    publicURL + "/api/v1/github/app/manifest/callback",
		"public":          false,
		"default_permissions": map[string]string{
			"metadata": "read", "contents": "write", "issues": "write", "pull_requests": "write", "actions": "read", "checks": "read",
		},
		"default_events": []string{"repository", "issues", "issue_comment", "pull_request", "pull_request_review", "pull_request_review_comment", "push"},
	}
}

func (s *Server) githubManifestCallback(c *gin.Context) {
	code := c.Query("code")
	state := c.Query("state")
	if code == "" || state == "" {
		badRequest(c, errors.New("当前 GitHub Manifest 回调缺少 code 或 state"))
		return
	}
	administratorID, err := s.redis.GetDel(c.Request.Context(), "tyrs-hand:github-manifest:"+state).Result()
	if err != nil || administratorID == "" {
		problem(c, http.StatusForbidden, "GitHub Manifest State 无效或已过期", err)
		return
	}
	endpoint := s.cfg.GitHubAPIURL + "/app-manifests/" + url.PathEscape(code) + "/conversions"
	request, _ := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, endpoint, bytes.NewReader(nil))
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		problem(c, http.StatusBadGateway, "GitHub Manifest 转换失败", err)
		return
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if response.StatusCode != http.StatusCreated {
		problem(c, http.StatusBadGateway, "GitHub Manifest 转换失败", fmt.Errorf("上游 GitHub 返回 %s", response.Status))
		return
	}
	var conversion struct {
		ID            int64  `json:"id"`
		ClientID      string `json:"client_id"`
		Slug          string `json:"slug"`
		PEM           string `json:"pem"`
		WebhookSecret string `json:"webhook_secret"`
		ClientSecret  string `json:"client_secret"`
	}
	if err := json.Unmarshal(body, &conversion); err != nil {
		problem(c, http.StatusBadGateway, "解析 GitHub Manifest 结果失败", err)
		return
	}
	if err := s.github.Save(c.Request.Context(), ghadapter.AppSettings{AppID: conversion.ID, ClientID: conversion.ClientID,
		AppSlug: conversion.Slug, PrivateKey: conversion.PEM, WebhookSecret: conversion.WebhookSecret,
		ClientSecret: conversion.ClientSecret}); err != nil {
		problem(c, http.StatusInternalServerError, "保存 GitHub App 失败", err)
		return
	}
	s.audit(c, "github_app.manifest.complete", "github_app", fmt.Sprint(conversion.ID), nil)
	_, _ = s.db.ExecContext(c.Request.Context(), `UPDATE audit_logs SET administrator_id = $2 WHERE request_id = $1 AND action = 'github_app.manifest.complete'`, c.GetString("request_id"), administratorID)
	c.Redirect(http.StatusFound, "/settings/github?configured=1")
}

func (s *Server) githubWebhook(c *gin.Context) {
	settings, _, provider, ok := s.github.Current()
	if !ok {
		problem(c, http.StatusServiceUnavailable, "GitHub App 尚未配置", nil)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 10<<20)
	payload, err := io.ReadAll(c.Request.Body)
	if err != nil {
		badRequest(c, err)
		return
	}
	service := orchestrator.NewWebhookService(s.db, provider, settings.AppSlug)
	result, err := service.Process(c.Request.Context(), c.GetHeader("X-Hub-Signature-256"), c.GetHeader("X-GitHub-Delivery"), c.GetHeader("X-GitHub-Event"), payload)
	if err != nil {
		problem(c, http.StatusUnauthorized, "Webhook 处理失败", err)
		return
	}
	c.JSON(http.StatusAccepted, result)
}

func (s *Server) internalToolCall(c *gin.Context) {
	var request toolservice.CallRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	_, app, _, ok := s.github.Current()
	if !ok {
		problem(c, http.StatusServiceUnavailable, "GitHub App 尚未配置", nil)
		return
	}
	result, err := toolservice.NewService(s.db, app, s.catalog).Call(c.Request.Context(), request)
	if err != nil {
		problem(c, http.StatusForbidden, "工具调用失败", err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (s *Server) internalGitCredential(c *gin.Context) {
	var request struct {
		Capability string `json:"capability" binding:"required"`
		Purpose    string `json:"purpose" binding:"required"`
		ThreadID   string `json:"threadId"`
		TurnID     string `json:"turnId"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	_, app, _, ok := s.github.Current()
	if !ok {
		problem(c, http.StatusServiceUnavailable, "GitHub App 尚未配置", nil)
		return
	}
	token, err := toolservice.NewService(s.db, app, s.catalog).GitCredential(c.Request.Context(), request.Capability, request.Purpose, request.TurnID)
	if err != nil {
		problem(c, http.StatusForbidden, "Git 凭据请求失败", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": token, "expiresInSeconds": 3600})
}

type repositoryRequest struct {
	InstallationExternalID int64  `json:"installationExternalId" binding:"required"`
	AccountLogin           string `json:"accountLogin" binding:"required"`
	AccountType            string `json:"accountType"`
	RepositoryExternalID   int64  `json:"repositoryExternalId" binding:"required"`
	Owner                  string `json:"owner" binding:"required"`
	Name                   string `json:"name" binding:"required"`
	DefaultBranch          string `json:"defaultBranch" binding:"required"`
	CloneURL               string `json:"cloneUrl" binding:"required"`
}

func (s *Server) createRepository(c *gin.Context) {
	var request repositoryRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	if request.AccountType == "" {
		request.AccountType = "Organization"
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "创建事务失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var installationID uuid.UUID
	err = tx.QueryRowContext(c, `
		INSERT INTO scm_installations(provider, external_id, account_login, account_type)
		VALUES ('github', $1, $2, $3)
		ON CONFLICT(provider, external_id) DO UPDATE SET account_login = EXCLUDED.account_login, account_type = EXCLUDED.account_type, updated_at = now()
		RETURNING id`, request.InstallationExternalID, request.AccountLogin, request.AccountType).Scan(&installationID)
	if err != nil {
		problem(c, http.StatusInternalServerError, "保存 Installation 失败", err)
		return
	}
	var repositoryID uuid.UUID
	err = tx.QueryRowContext(c, `
		INSERT INTO repositories(installation_id, provider, external_id, owner, name, default_branch, clone_url)
		VALUES ($1, 'github', $2, $3, $4, $5, $6)
		ON CONFLICT(provider, external_id) DO UPDATE SET installation_id = EXCLUDED.installation_id,
			owner = EXCLUDED.owner, name = EXCLUDED.name, default_branch = EXCLUDED.default_branch,
			clone_url = EXCLUDED.clone_url, updated_at = now()
		RETURNING id`, installationID, request.RepositoryExternalID, request.Owner, request.Name, request.DefaultBranch, request.CloneURL).Scan(&repositoryID)
	if err != nil {
		problem(c, http.StatusInternalServerError, "保存仓库失败", err)
		return
	}
	if err := orchestrator.SeedRepositoryRules(c, tx, repositoryID); err != nil {
		problem(c, http.StatusInternalServerError, "创建默认规则失败", err)
		return
	}
	if err := tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "提交仓库配置失败", err)
		return
	}
	s.audit(c, "repository.create", "repository", repositoryID.String(), map[string]any{"owner": request.Owner, "name": request.Name})
	c.JSON(http.StatusCreated, gin.H{"id": repositoryID})
}

func (s *Server) listRepositories(c *gin.Context) {
	s.listRows(c, `SELECT id, owner, name, default_branch, enabled, updated_at FROM repositories ORDER BY owner, name`, []string{"id", "owner", "name", "defaultBranch", "enabled", "updatedAt"})
}
