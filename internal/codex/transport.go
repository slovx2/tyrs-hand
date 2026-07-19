package codex

import (
	"io"
	"os"
	"os/exec"
)

type ProcessSpec struct {
	Bin  string
	Args []string
	Dir  string
	Env  []string
}

type Process interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	Signal(os.Signal) error
	Kill() error
	Wait() error
}

type Launcher interface {
	Launch(ProcessSpec) (Process, error)
}

type ExecLauncher struct{}

func (ExecLauncher) Launch(spec ProcessSpec) (Process, error) {
	cmd := exec.Command(spec.Bin, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execProcess{cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr}, nil
}

type execProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
}

func (p *execProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *execProcess) Stdout() io.ReadCloser { return p.stdout }
func (p *execProcess) Stderr() io.ReadCloser { return p.stderr }
func (p *execProcess) Wait() error           { return p.cmd.Wait() }
func (p *execProcess) Signal(signal os.Signal) error {
	if p.cmd.Process == nil {
		return os.ErrProcessDone
	}
	return p.cmd.Process.Signal(signal)
}
func (p *execProcess) Kill() error {
	if p.cmd.Process == nil {
		return os.ErrProcessDone
	}
	return p.cmd.Process.Kill()
}
