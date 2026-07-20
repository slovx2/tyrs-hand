package hostdocker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

type wrapperScope struct {
	Scope
	LeaseRoot string
	Network   string
}

func wrapperScopeFromEnvironment() (wrapperScope, bool) {
	scope := wrapperScope{
		Scope: Scope{
			WorkspaceID: os.Getenv(envWorkspaceID),
			IntentID:    os.Getenv(envIntentID),
			RunID:       os.Getenv(envRunID),
		},
		LeaseRoot: os.Getenv(envLeaseRoot),
		Network:   os.Getenv("TYRS_HAND_DOCKER_NETWORK"),
	}
	return scope, scope.LeaseRoot != "" && scope.Network != "" && scope.validate() == nil
}

type preparedCommand struct {
	Arguments    []string
	TouchTargets []string
}

func prepareArguments(arguments []string, scope wrapperScope) preparedCommand {
	result := preparedCommand{Arguments: append([]string{}, arguments...)}
	commandIndex := locateCommand(arguments, 0)
	if commandIndex < 0 {
		return result
	}
	primary := arguments[commandIndex]
	actionIndex := commandIndex
	action := primary
	if primary == "container" || primary == "volume" || primary == "network" {
		actionIndex = locateCommand(arguments, commandIndex+1)
		if actionIndex < 0 {
			return result
		}
		action = arguments[actionIndex]
	}

	switch {
	case (primary == "run" || primary == "create") || (primary == "container" && (action == "run" || action == "create")):
		injected := labelArguments(scope.Scope)
		if !hasNetwork(arguments[actionIndex+1:]) {
			injected = append(injected, "--network", scope.Network)
		}
		result.Arguments = insertArguments(arguments, actionIndex+1, injected)
	case (primary == "volume" || primary == "network") && action == "create":
		result.Arguments = insertArguments(arguments, actionIndex+1, labelArguments(scope.Scope))
	case primary == "start" || primary == "restart" || primary == "unpause":
		result.TouchTargets = positionalTargets(arguments[actionIndex+1:], action)
	case primary == "exec":
		result.TouchTargets = firstPositional(arguments[actionIndex+1:], action)
	case primary == "container" && (action == "start" || action == "restart" || action == "unpause"):
		result.TouchTargets = positionalTargets(arguments[actionIndex+1:], action)
	case primary == "container" && action == "exec":
		result.TouchTargets = firstPositional(arguments[actionIndex+1:], action)
	}
	return result
}

func locateCommand(arguments []string, start int) int {
	valueOptions := map[string]bool{
		"--config": true, "--context": true, "--host": true, "-H": true,
		"--log-level": true, "-l": true, "--tlscacert": true, "--tlscert": true, "--tlskey": true,
	}
	for index := start; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "--" && index+1 < len(arguments) {
			return index + 1
		}
		if !strings.HasPrefix(argument, "-") {
			return index
		}
		if valueOptions[argument] {
			index++
		}
	}
	return -1
}

func labelArguments(scope Scope) []string {
	result := make([]string, 0, len(scope.labels())*2)
	for _, label := range scope.labels() {
		result = append(result, "--label", label)
	}
	return result
}

func insertArguments(arguments []string, index int, values []string) []string {
	result := make([]string, 0, len(arguments)+len(values))
	result = append(result, arguments[:index]...)
	result = append(result, values...)
	return append(result, arguments[index:]...)
}

func hasNetwork(arguments []string) bool {
	for _, argument := range arguments {
		if argument == "--network" || argument == "--net" || strings.HasPrefix(argument, "--network=") || strings.HasPrefix(argument, "--net=") {
			return true
		}
	}
	return false
}

func positionalTargets(arguments []string, action string) []string {
	valueOptions := actionValueOptions(action)
	var result []string
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if strings.HasPrefix(argument, "-") {
			if valueOptions[argument] {
				index++
			}
			continue
		}
		result = append(result, argument)
	}
	return result
}

func firstPositional(arguments []string, action string) []string {
	targets := positionalTargets(arguments, action)
	if len(targets) == 0 {
		return nil
	}
	return targets[:1]
}

func actionValueOptions(action string) map[string]bool {
	switch action {
	case "start":
		return map[string]bool{"--checkpoint": true, "--checkpoint-dir": true, "--detach-keys": true}
	case "restart":
		return map[string]bool{"--signal": true, "--time": true, "-t": true}
	case "exec":
		return map[string]bool{
			"--detach-keys": true, "--env": true, "-e": true, "--env-file": true,
			"--user": true, "-u": true, "--workdir": true, "-w": true,
		}
	default:
		return map[string]bool{}
	}
}

func RunWrapper(ctx context.Context, arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	realBinary := os.Getenv("TYRS_HAND_DOCKER_REAL_BIN")
	if realBinary == "" {
		realBinary = RealDockerBinary
	}
	scope, managed := wrapperScopeFromEnvironment()
	prepared := preparedCommand{Arguments: append([]string{}, arguments...)}
	if managed {
		prepared = prepareArguments(arguments, scope)
	}
	var reserved []string
	if managed && len(prepared.TouchTargets) > 0 {
		containers, resolveErr := resolveTouchedContainers(ctx, realBinary, prepared.TouchTargets)
		if resolveErr == nil {
			reserved, resolveErr = reserveContainers(scope.LeaseRoot, scope.RunID, containers)
		}
		if resolveErr != nil && stderr != nil {
			_, _ = fmt.Fprintf(stderr, "tyrs-hand: 记录 Docker 容器使用关系失败，自动停止可能延迟: %v\n", resolveErr)
		}
	}
	command := exec.CommandContext(ctx, realBinary, prepared.Arguments...)
	command.Stdin, command.Stdout, command.Stderr = stdin, stdout, stderr
	err := command.Run()
	if err != nil && len(reserved) > 0 {
		_ = releaseContainers(scope.LeaseRoot, scope.RunID, reserved)
	}
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	_, _ = fmt.Fprintf(stderr, "tyrs-hand: 启动 Docker CLI 失败: %v\n", err)
	return 127
}

func resolveTouchedContainers(ctx context.Context, binary string, targets []string) ([]string, error) {
	arguments := []string{"container", "inspect", "--format", "{{.Id}}"}
	arguments = append(arguments, targets...)
	command := exec.CommandContext(ctx, binary, arguments...)
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(output)), nil
}
