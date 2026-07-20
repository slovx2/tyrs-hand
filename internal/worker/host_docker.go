package worker

import (
	"context"
	"encoding/json"

	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/hostdocker"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"go.uber.org/zap"
)

const hostDockerDeveloperInstructions = `Host Docker Beta is enabled for this run. Use the docker CLI wrapper from PATH. ` +
	`Use TYRS_HAND_DOCKER_NAME_PREFIX for names and the default TYRS_HAND_DOCKER_NETWORK for sibling services. ` +
	`Connect to service containers by container name; localhost refers to the Worker. ` +
	`Do not use docker compose. docker build contexts work, but bind-mounting the current workspace with -v "$PWD:..." is unsupported. ` +
	`Containers used by this run are stopped, not deleted, when the run ends.`

type dockerRunSession interface {
	Environment() []string
	Close(context.Context) error
}

func (p *Processor) beginHostDocker(claimed *codexcontrol.ClaimedControl, workspaceID string) (dockerRunSession, error) {
	if p.hostDocker == nil {
		return nil, nil
	}
	return p.hostDocker.Begin(hostdocker.Scope{
		WorkspaceID: workspaceID,
		IntentID:    claimed.ID.String(),
		RunID:       claimed.RunID.String(),
	})
}

func (p *Processor) finishHostDocker(session dockerRunSession, claimed *codexcontrol.ClaimedControl) {
	if session == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.cfg.DockerCleanupTimeout)
	defer cancel()
	if err := session.Close(ctx); err != nil {
		p.logger.Warn("Host Docker 自动停止失败，将由后台扫描重试",
			zap.String("intent_id", claimed.ID.String()), zap.String("run_id", claimed.RunID.String()), zap.Error(err))
	}
}

func hostDockerAdditionalContext(session dockerRunSession) map[string]ports.AdditionalContextEntry {
	if session == nil {
		return nil
	}
	environment := session.Environment()
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		key, value, found := cutEnvironment(entry)
		if found && key != "TYRS_HAND_DOCKER_LEASE_ROOT" {
			values[key] = value
		}
	}
	payload, _ := json.Marshal(map[string]any{
		"status": "beta", "environment": values,
		"warning": "Host Docker 拥有宿主 Daemon 的完整权限，仅可在所有用户和仓库可信时使用。",
		"cleanup": "Run 结束后停止受管容器，但不删除容器、Network、Volume、镜像或 Build Cache。",
	})
	return map[string]ports.AdditionalContextEntry{
		"tyrs_hand_host_docker": {Kind: "application", Value: string(payload)},
	}
}

func cutEnvironment(entry string) (string, string, bool) {
	for index := 0; index < len(entry); index++ {
		if entry[index] == '=' {
			return entry[:index], entry[index+1:], index > 0
		}
	}
	return "", "", false
}

func dockerInstructions(session dockerRunSession) string {
	if session == nil {
		return ""
	}
	return "\n" + hostDockerDeveloperInstructions
}
