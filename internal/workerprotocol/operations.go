package workerprotocol

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

const privateWorkerRoutePrefix = "/worker/v1/"

type operationDefinition struct {
	name     string
	method   string
	segments []string
}

var operationDefinitions = []operationDefinition{
	{name: "worker.heartbeat", method: http.MethodPost, segments: []string{"heartbeat"}},
	{name: "worker.claim", method: http.MethodPost, segments: []string{"claims"}},
	{name: "ssh.configuration", method: http.MethodGet, segments: []string{"ssh-configuration"}},
	{name: "development.environments.list", method: http.MethodGet, segments: []string{"development-environments"}},
	{name: "development.environment.daemon-state", method: http.MethodPost, segments: []string{"development-environments", "{id}", "daemon-state"}},
	{name: "development.environment.projects.snapshot", method: http.MethodPost, segments: []string{"development-environments", "{id}", "projects", "snapshot"}},
	{name: "development.environment.interactive.interrupted", method: http.MethodPost, segments: []string{"development-environments", "{id}", "interactive", "interrupted"}},
	{name: "desktop.thread.prepare", method: http.MethodPost, segments: []string{"desktop-thread-requests"}},
	{name: "desktop.thread.state", method: http.MethodGet, segments: []string{"desktop-thread-requests", "{id}"}},
	{name: "desktop.thread.complete", method: http.MethodPost, segments: []string{"desktop-thread-requests", "{id}", "complete"}},
	{name: "desktop.thread.fail", method: http.MethodPost, segments: []string{"desktop-thread-requests", "{id}", "fail"}},
	{name: "thread.metadata.record", method: http.MethodPost, segments: []string{"thread-metadata-events"}},
	{name: "thread.names.pending", method: http.MethodGet, segments: []string{"thread-name-updates"}},
	{name: "thread.name.ack", method: http.MethodPost, segments: []string{"thread-name-updates", "{id}", "ack"}},
	{name: "thread.lifecycle.prepare", method: http.MethodPost, segments: []string{"thread-lifecycle-requests", "desktop"}},
	{name: "thread.lifecycle.pending", method: http.MethodGet, segments: []string{"thread-lifecycle-requests"}},
	{name: "thread.lifecycle.state", method: http.MethodGet, segments: []string{"thread-lifecycle-requests", "{id}"}},
	{name: "thread.lifecycle.complete", method: http.MethodPost, segments: []string{"thread-lifecycle-requests", "{id}", "complete"}},
	{name: "desktop.turn.prepare", method: http.MethodPost, segments: []string{"desktop-turns"}},
	{name: "desktop.turn.preflight", method: http.MethodPost, segments: []string{"desktop-turns", "preflight"}},
	{name: "desktop.image.target", method: http.MethodGet, segments: []string{"desktop-turns", "{id}", "images", "target"}},
	{name: "desktop.image.fail", method: http.MethodPost, segments: []string{"desktop-turns", "{id}", "images", "{ordinal}", "fail"}},
	{name: "desktop.rollback.prepare", method: http.MethodPost, segments: []string{"desktop-rollbacks"}},
	{name: "desktop.rollback.complete", method: http.MethodPost, segments: []string{"desktop-rollbacks", "{id}", "complete"}},
	{name: "desktop.steer.record", method: http.MethodPost, segments: []string{"desktop-steers"}},
	{name: "interactive.state", method: http.MethodGet, segments: []string{"interactive", "{id}"}},
	{name: "interactive.answer", method: http.MethodPost, segments: []string{"interactive", "answer"}},
	{name: "run.interactive.register", method: http.MethodPost, segments: []string{"runs", "{id}", "interactive"}},
	{name: "run.heartbeat", method: http.MethodPost, segments: []string{"runs", "{id}", "heartbeat"}},
	{name: "run.command.ack", method: http.MethodPost, segments: []string{"runs", "{id}", "commands", "ack"}},
	{name: "run.events.append", method: http.MethodPost, segments: []string{"runs", "{id}", "events"}},
	{name: "run.complete", method: http.MethodPost, segments: []string{"runs", "{id}", "complete"}},
	{name: "run.fail", method: http.MethodPost, segments: []string{"runs", "{id}", "fail"}},
	{name: "run.thread.set", method: http.MethodPost, segments: []string{"runs", "{id}", "thread"}},
	{name: "run.submission.record", method: http.MethodPost, segments: []string{"runs", "{id}", "submission"}},
	{name: "run.turn.confirm", method: http.MethodPost, segments: []string{"runs", "{id}", "confirm"}},
	{name: "run.development-state.update", method: http.MethodPost, segments: []string{"runs", "{id}", "development-state"}},
	{name: "run.workspace-state.update", method: http.MethodPost, segments: []string{"runs", "{id}", "workspace-state"}},
	{name: "run.tool.call", method: http.MethodPost, segments: []string{"runs", "{id}", "tools", "call"}},
	{name: "run.git-credential.get", method: http.MethodPost, segments: []string{"runs", "{id}", "git-credential"}},
}

func ResolveOperation(method, path string) (string, map[string]string, error) {
	if !strings.HasPrefix(path, privateWorkerRoutePrefix) || strings.Contains(path, "?") {
		return "", nil, errors.New("worker 私有操作路径无效")
	}
	segments := strings.Split(strings.TrimPrefix(path, privateWorkerRoutePrefix), "/")
	for _, definition := range operationDefinitions {
		if definition.method != method || len(definition.segments) != len(segments) {
			continue
		}
		parameters := make(map[string]string)
		matched := true
		for index, expected := range definition.segments {
			if strings.HasPrefix(expected, "{") {
				parameters[strings.Trim(expected, "{}")] = segments[index]
				continue
			}
			if expected != segments[index] {
				matched = false
				break
			}
		}
		if matched {
			return definition.name, parameters, nil
		}
	}
	return "", nil, fmt.Errorf("worker 操作未注册: %s %s", method, path)
}

func ResolveOperationRoute(operation string, parameters map[string]string) (string, string, error) {
	for _, definition := range operationDefinitions {
		if definition.name != operation {
			continue
		}
		segments := append([]string(nil), definition.segments...)
		for index, segment := range segments {
			if !strings.HasPrefix(segment, "{") {
				continue
			}
			name := strings.Trim(segment, "{}")
			value := strings.TrimSpace(parameters[name])
			if err := validateOperationParameter(name, value); err != nil {
				return "", "", err
			}
			segments[index] = value
		}
		return definition.method, privateWorkerRoutePrefix + strings.Join(segments, "/"), nil
	}
	return "", "", fmt.Errorf("未知 Worker 操作 %q", operation)
}

func validateOperationParameter(name, value string) error {
	switch name {
	case "id":
		if _, err := uuid.Parse(value); err != nil {
			return errors.New("worker 操作缺少有效资源 ID")
		}
	case "ordinal":
		ordinal, err := strconv.Atoi(value)
		if err != nil || ordinal < 0 {
			return errors.New("worker 操作缺少有效序号")
		}
	default:
		return fmt.Errorf("未知 Worker 操作参数 %q", name)
	}
	return nil
}
