package inferenceservicecheck

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCheckPreflightAllowsFreeListener(t *testing.T) {
	dependencies := testDependencies([]int{}, nil, nil)
	dependencies.ProbeBind = func(context.Context, string) error { return nil }
	if err := Check(context.Background(), PhasePreflight, "[::1]:5401", 0, dependencies); err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
}

func TestCheckPreflightRejectsBindRaceWithoutOwner(t *testing.T) {
	dependencies := testDependencies([]int{}, nil, nil)
	err := Check(context.Background(), PhasePreflight, "[::1]:5401", 41, dependencies)
	if err == nil || !strings.Contains(err.Error(), "does not own") {
		t.Fatalf("error=%v, want ownership race failure", err)
	}
}

func TestCheckPreflightAllowsUnrelatedCoexistingListener(t *testing.T) {
	dependencies := testDependencies([]int{82}, nil, nil)
	dependencies.ProbeBind = func(context.Context, string) error { return nil }
	dependencies.ListenerPIDs = func(context.Context, string, int) ([]int, error) {
		t.Fatal("ownership lookup ran after successful exact bind probe")
		return nil, nil
	}
	if err := Check(context.Background(), PhasePreflight, "127.0.0.1:5401", 41, dependencies); err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
}

func TestCheckPreflightRejectsForeignListener(t *testing.T) {
	dependencies := testDependencies([]int{82}, nil, nil)
	err := Check(context.Background(), PhasePreflight, "[::1]:5401", 41, dependencies)
	if err == nil || !strings.Contains(err.Error(), "PID 82") {
		t.Fatalf("error=%v, want foreign PID detail", err)
	}
}

func TestCheckPreflightAllowsExpectedServiceListener(t *testing.T) {
	dependencies := testDependencies([]int{41}, nil, nil)
	if err := Check(context.Background(), PhasePreflight, "[::1]:5401", 41, dependencies); err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
}

func TestCheckPostRestartRejectsMissingServicePID(t *testing.T) {
	dependencies := testDependencies([]int{41}, nil, nil)
	err := Check(context.Background(), PhasePostRestart, "[::1]:5401", 0, dependencies)
	if err == nil || !strings.Contains(err.Error(), "service PID") {
		t.Fatalf("error=%v, want missing service PID detail", err)
	}
}

func TestCheckPostRestartReturnsHealthFailure(t *testing.T) {
	healthErr := errors.New("not serving")
	dependencies := testDependencies([]int{41}, healthErr, nil)
	err := Check(context.Background(), PhasePostRestart, "[::1]:5401", 41, dependencies)
	if !errors.Is(err, healthErr) {
		t.Fatalf("error=%v, want errors.Is(%v)", err, healthErr)
	}
}

func TestCheckPostRestartAcceptsOwnedHealthyListener(t *testing.T) {
	dependencies := testDependencies([]int{41}, nil, nil)
	if err := Check(context.Background(), PhasePostRestart, "[::1]:5401", 41, dependencies); err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
}

func TestReadPIDLinesDeduplicatesAndSorts(t *testing.T) {
	processIDs, err := readPIDLines(context.Background(), "82\n41\n82\n")
	if err != nil {
		t.Fatalf("readPIDLines returned error: %v", err)
	}
	if len(processIDs) != 2 || processIDs[0] != 41 || processIDs[1] != 82 {
		t.Fatalf("process IDs=%v, want [41 82]", processIDs)
	}
}

func TestReadListenerPIDsRejectsHostname(t *testing.T) {
	_, err := readListenerPIDs(context.Background(), "localhost:5401", 0)
	if err == nil || !strings.Contains(err.Error(), "literal IPv4 or IPv6") {
		t.Fatalf("error=%v, want literal address requirement", err)
	}
}

func testDependencies(
	listenerPIDs []int,
	healthErr error,
	listenerErr error,
) Dependencies {
	return Dependencies{
		ListenerPIDs: func(context.Context, string, int) ([]int, error) {
			return listenerPIDs, listenerErr
		},
		CheckHealth: func(context.Context, string) error {
			return healthErr
		},
		ProbeBind: func(context.Context, string) error {
			return errors.New("address already in use")
		},
	}
}
