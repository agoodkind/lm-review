package main

import "testing"

func TestInferenceAndDeprecatedJudgeCommandsShareGenericRunner(t *testing.T) {
	inferenceCommand := newInferenceCmd()
	if inferenceCommand.Use != "inference" || inferenceCommand.Hidden {
		t.Fatalf("inference command use=%q hidden=%t", inferenceCommand.Use, inferenceCommand.Hidden)
	}

	judgeCommand := newJudgeAliasCmd()
	if judgeCommand.Use != "judge" || !judgeCommand.Hidden {
		t.Fatalf("judge alias use=%q hidden=%t", judgeCommand.Use, judgeCommand.Hidden)
	}
	if judgeCommand.Short != inferenceCommand.Short {
		t.Fatalf("judge alias short=%q, want %q", judgeCommand.Short, inferenceCommand.Short)
	}
}
