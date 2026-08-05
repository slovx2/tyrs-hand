package codexcontrol

import (
	"strings"
)

const (
	PlanExecutionDisplayText = "同意并执行计划"
	planExecutePrefix        = "PLEASE IMPLEMENT THIS PLAN:"
)

func PlanExecutionInstruction(plan string) string {
	return planExecutePrefix + "\n" + plan
}

// DisplayInstruction 与 Codex Desktop 使用同一前缀识别官方 Plan 执行动作。
func DisplayInstruction(instruction string) string {
	if strings.HasPrefix(instruction, planExecutePrefix) {
		return PlanExecutionDisplayText
	}
	return instruction
}
