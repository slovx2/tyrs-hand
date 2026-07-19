package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/slovx2/tyrs-hand/internal/devenv"
	"go.uber.org/zap"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch os.Args[1] {
	case "prepare":
		prepare(ctx, os.Args[2:])
	case "exec":
		execute(ctx, os.Args[2:])
	default:
		usage()
	}
}

func prepare(ctx context.Context, arguments []string) {
	flags := flag.NewFlagSet("prepare", flag.ExitOnError)
	dataRoot := flags.String("data-root", "/data/worker", "Worker 数据目录")
	workspace := flags.String("workspace", "", "Workspace 路径")
	requireReady := flags.Bool("require-ready", false, "degraded 时返回非零状态")
	must(flags.Parse(arguments))
	result := prepareWorkspace(ctx, *dataRoot, *workspace)
	must(json.NewEncoder(os.Stdout).Encode(result))
	if *requireReady && result.Status != "ready" {
		os.Exit(1)
	}
}

func execute(ctx context.Context, arguments []string) {
	separator := -1
	for index, argument := range arguments {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator == len(arguments)-1 {
		usage()
	}
	flags := flag.NewFlagSet("exec", flag.ExitOnError)
	dataRoot := flags.String("data-root", "/data/worker", "Worker 数据目录")
	workspace := flags.String("workspace", "", "Workspace 路径")
	cwd := flags.String("cwd", ".", "相对于 Workspace 的命令目录")
	must(flags.Parse(arguments[:separator]))
	result := prepareWorkspace(ctx, *dataRoot, *workspace)
	if result.Status != "ready" {
		must(json.NewEncoder(os.Stderr).Encode(result))
		os.Exit(1)
	}
	commandName, err := executableFromEnvironment(arguments[separator+1], result.Environment)
	must(err)
	command := exec.CommandContext(ctx, commandName, arguments[separator+2:]...)
	command.Dir = filepath.Join(*workspace, filepath.FromSlash(*cwd))
	command.Env = append(os.Environ(), result.Environment...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	must(command.Run())
}

func executableFromEnvironment(name string, environment []string) (string, error) {
	if strings.ContainsRune(name, filepath.Separator) {
		return name, nil
	}
	pathValue := os.Getenv("PATH")
	for _, value := range environment {
		if strings.HasPrefix(value, "PATH=") {
			pathValue = strings.TrimPrefix(value, "PATH=")
		}
	}
	for _, directory := range filepath.SplitList(pathValue) {
		candidate := filepath.Join(directory, name)
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("在准备后的 PATH 中找不到可执行文件 %q", name)
}

func prepareWorkspace(ctx context.Context, dataRoot, workspace string) devenv.Result {
	if workspace == "" {
		fmt.Fprintln(os.Stderr, "必须提供 --workspace")
		os.Exit(2)
	}
	manager, err := devenv.NewManager(dataRoot, zap.NewNop())
	must(err)
	return manager.Prepare(ctx, workspace)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage: tyrs-hand-runtime prepare|exec --data-root <path> --workspace <path> [-- command]")
	os.Exit(2)
}
