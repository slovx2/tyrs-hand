package codexcontrol

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlanExecutionInstructionUsesDesktopContract(t *testing.T) {
	plan := "# 实施计划\n\n- 第一步\n- 第二步"
	instruction := PlanExecutionInstruction(plan)
	require.Equal(t, planExecutePrefix+"\n"+plan, instruction)
	require.Equal(t, PlanExecutionDisplayText, DisplayInstruction(instruction))
	require.Equal(t, PlanExecutionDisplayText,
		DisplayInstruction(planExecutePrefix+" 手动正文"))
	require.Equal(t, "普通消息", DisplayInstruction("普通消息"))
}
