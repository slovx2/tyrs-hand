package codex

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
)

type scriptedLauncher struct {
	mu           sync.Mutex
	launches     int
	specs        []ProcessSpec
	script       func(*scriptedServer)
	processes    []*scriptedProcess
	ignoreSignal bool
}

func (l *scriptedLauncher) Launch(spec ProcessSpec) (Process, error) {
	serverIn, clientIn := io.Pipe()
	clientOut, serverOut := io.Pipe()
	clientErr, serverErr := io.Pipe()
	process := &scriptedProcess{
		stdin: clientIn, stdout: clientOut, stderr: clientErr,
		serverIn: serverIn, serverOut: serverOut, serverErr: serverErr,
		exited:       make(chan struct{}),
		ignoreSignal: l.ignoreSignal,
	}
	l.mu.Lock()
	l.launches++
	l.specs = append(l.specs, spec)
	l.processes = append(l.processes, process)
	l.mu.Unlock()
	go func() {
		l.script(&scriptedServer{process: process, scanner: bufio.NewScanner(serverIn)})
		process.exit(nil)
	}()
	return process, nil
}

type scriptedProcess struct {
	stdin        io.WriteCloser
	stdout       io.ReadCloser
	stderr       io.ReadCloser
	serverIn     *io.PipeReader
	serverOut    *io.PipeWriter
	serverErr    *io.PipeWriter
	exited       chan struct{}
	once         sync.Once
	err          error
	mu           sync.Mutex
	signals      int
	kills        int
	ignoreSignal bool
}

func (p *scriptedProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *scriptedProcess) Stdout() io.ReadCloser { return p.stdout }
func (p *scriptedProcess) Stderr() io.ReadCloser { return p.stderr }
func (p *scriptedProcess) Signal(os.Signal) error {
	p.mu.Lock()
	p.signals++
	p.mu.Unlock()
	if !p.ignoreSignal {
		p.exit(nil)
	}
	return nil
}
func (p *scriptedProcess) Kill() error {
	p.mu.Lock()
	p.kills++
	p.mu.Unlock()
	p.exit(errors.New("killed"))
	return nil
}
func (p *scriptedProcess) Wait() error {
	<-p.exited
	return p.err
}
func (p *scriptedProcess) exit(err error) {
	p.once.Do(func() {
		p.err = err
		_ = p.serverOut.Close()
		_ = p.serverErr.Close()
		_ = p.serverIn.Close()
		close(p.exited)
	})
}

type scriptedServer struct {
	process *scriptedProcess
	scanner *bufio.Scanner
}

func (s *scriptedServer) request() map[string]any {
	if !s.scanner.Scan() {
		return nil
	}
	var value map[string]any
	_ = json.Unmarshal(s.scanner.Bytes(), &value)
	return value
}

func (s *scriptedServer) send(value any) {
	_ = json.NewEncoder(s.process.serverOut).Encode(value)
}

func (s *scriptedServer) raw(value string) {
	_, _ = io.WriteString(s.process.serverOut, value)
}

func initializeScript(next func(*scriptedServer)) func(*scriptedServer) {
	return func(server *scriptedServer) {
		request := server.request()
		server.send(map[string]any{"id": request["id"], "result": map[string]any{}})
		_ = server.request()
		next(server)
	}
}
