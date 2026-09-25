// Package runtimeidentity 定义 Worker、Control 与客户端共同使用的引擎身份。
package runtimeidentity

import (
	"encoding/json"
	"fmt"
)

type Engine string

const (
	Codex  Engine = "codex"
	Claude Engine = "claude-code"
)

func (e Engine) Validate() error {
	if e != Codex && e != Claude {
		return fmt.Errorf("未知运行时引擎 %q", e)
	}
	return nil
}

func (e *Engine) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	engine := Engine(value)
	if err := engine.Validate(); err != nil {
		return err
	}
	*e = engine
	return nil
}

// Identity 不使用 model 或 thread ID 推断引擎。
type Identity struct {
	WorkerID string `json:"workerId"`
	Engine   Engine `json:"engine"`
}

func (i Identity) Validate() error {
	if i.WorkerID == "" {
		return fmt.Errorf("必须提供 Worker ID")
	}
	return i.Engine.Validate()
}
