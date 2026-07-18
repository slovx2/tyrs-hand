package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/slovx2/tyrs-hand/internal/domain"
)

func VerifySignature(secret []byte, signature string, payload []byte) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(signature, prefix) || len(secret) == 0 {
		return false
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(signature, prefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payload)
	return hmac.Equal(provided, mac.Sum(nil))
}

type webhookEnvelope struct {
	Action       string `json:"action"`
	Installation struct {
		ID      int64 `json:"id"`
		Account struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"account"`
	} `json:"installation"`
	Repository struct {
		ID            int64  `json:"id"`
		Name          string `json:"name"`
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
		CloneURL      string `json:"clone_url"`
		Owner         struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
	Sender struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"sender"`
	Issue struct {
		Number      int       `json:"number"`
		Title       string    `json:"title"`
		Body        string    `json:"body"`
		PullRequest *struct{} `json:"pull_request"`
	} `json:"issue"`
	PullRequest struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
	Comment struct {
		Body string `json:"body"`
	} `json:"comment"`
	Review struct {
		Body string `json:"body"`
	} `json:"review"`
	Repositories        []webhookRepository `json:"repositories"`
	RepositoriesAdded   []webhookRepository `json:"repositories_added"`
	RepositoriesRemoved []webhookRepository `json:"repositories_removed"`
}

type webhookRepository struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	CloneURL      string `json:"clone_url"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
}

func NormalizeWebhook(deliveryID, eventName string, payload []byte) (domain.NormalizedEvent, error) {
	if deliveryID == "" || eventName == "" {
		return domain.NormalizedEvent{}, errors.New("收到的 Webhook 缺少 delivery ID 或 event name")
	}
	var envelope webhookEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return domain.NormalizedEvent{}, fmt.Errorf("解析 GitHub Webhook: %w", err)
	}
	owner := envelope.Repository.Owner.Login
	if owner == "" && envelope.Repository.FullName != "" {
		owner, _, _ = strings.Cut(envelope.Repository.FullName, "/")
	}
	event := domain.NormalizedEvent{
		Provider: "github", DeliveryID: deliveryID, EventName: eventName,
		Action: envelope.Action, InstallationID: envelope.Installation.ID,
		RepositoryID: envelope.Repository.ID, Owner: owner, Repository: envelope.Repository.Name,
		Actor: envelope.Sender.Login, ActorID: envelope.Sender.ID,
		Raw: append([]byte(nil), payload...), ReceivedAt: time.Now().UTC(),
	}
	event.Installation.AccountLogin = envelope.Installation.Account.Login
	event.Installation.AccountType = envelope.Installation.Account.Type
	for _, repository := range append(envelope.Repositories, envelope.RepositoriesAdded...) {
		repositoryOwner := repository.Owner.Login
		if repositoryOwner == "" {
			repositoryOwner, _, _ = strings.Cut(repository.FullName, "/")
		}
		event.Installation.Repositories = append(event.Installation.Repositories, domain.SCMRepository{
			ExternalID: repository.ID, Owner: repositoryOwner, Name: repository.Name,
			DefaultBranch: repository.DefaultBranch, CloneURL: repository.CloneURL,
		})
	}
	if eventName == "repository" && envelope.Repository.ID > 0 {
		event.Installation.Repositories = append(event.Installation.Repositories, domain.SCMRepository{
			ExternalID: envelope.Repository.ID, Owner: owner, Name: envelope.Repository.Name,
			DefaultBranch: envelope.Repository.DefaultBranch, CloneURL: envelope.Repository.CloneURL,
		})
	}
	for _, repository := range envelope.RepositoriesRemoved {
		event.Installation.RemovedRepositoryIDs = append(event.Installation.RemovedRepositoryIDs, repository.ID)
	}
	switch {
	case envelope.PullRequest.Number > 0:
		event.Kind = domain.WorkItemPullRequest
		event.Number = envelope.PullRequest.Number
		event.Title = envelope.PullRequest.Title
		event.Body = firstNonEmpty(envelope.Comment.Body, envelope.Review.Body, envelope.PullRequest.Body)
		event.HeadSHA = envelope.PullRequest.Head.SHA
	case envelope.Issue.Number > 0 && envelope.Issue.PullRequest != nil:
		// PR 的普通评论由 issue_comment 事件承载，只能通过 issue.pull_request 标记识别。
		event.Kind = domain.WorkItemPullRequest
		event.Number = envelope.Issue.Number
		event.Title = envelope.Issue.Title
		event.Body = firstNonEmpty(envelope.Comment.Body, envelope.Issue.Body)
	case envelope.Issue.Number > 0:
		event.Kind = domain.WorkItemIssue
		event.Number = envelope.Issue.Number
		event.Title = envelope.Issue.Title
		event.Body = firstNonEmpty(envelope.Comment.Body, envelope.Issue.Body)
	}
	return event, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
