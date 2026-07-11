package main

import "testing"

func TestRootCommandExposesInferenceWithoutJudgeAlias(t *testing.T) {
	root := newRootCmd()
	if command, _, err := root.Find([]string{"inference"}); err != nil || command.Name() != "inference" {
		t.Fatalf("find inference command=%v err=%v", command, err)
	}
	if command, _, err := root.Find([]string{"judge"}); err == nil && command.Name() == "judge" {
		t.Fatalf("deprecated judge alias remains registered: %v", command)
	}
}
