// Package inferenceservicecheck verifies supervised inference listener handoff.
package inferenceservicecheck

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"goodkind.io/lm-review/api/inferencepb"
)

const (
	// PhasePreflight verifies that the configured listener is free or service-owned.
	PhasePreflight = "preflight"
	// PhasePostRestart verifies ownership and gRPC health after service restart.
	PhasePostRestart = "post-restart"
)

type operatingSystem string

const (
	operatingSystemDarwin operatingSystem = "darwin"
	operatingSystemLinux  operatingSystem = "linux"
)

var processIDPattern = regexp.MustCompile(`pid=(\d+)`)

// Dependencies contains externally observable checks used by Check.
type Dependencies struct {
	ListenerPIDs func(context.Context, string) ([]int, error)
	CheckHealth  func(context.Context, string) error
}

// DefaultDependencies returns platform listener lookup and gRPC health checks.
func DefaultDependencies() Dependencies {
	return Dependencies{
		ListenerPIDs: readListenerPIDs,
		CheckHealth:  readHealth,
	}
}

// Check verifies a deployment handoff phase without terminating any process.
func Check(
	ctx context.Context,
	phase string,
	listenAddress string,
	expectedPID int,
	dependencies Dependencies,
) error {
	slog.InfoContext(
		ctx,
		"inference.service_check.begin",
		"phase", phase,
		"listen_address", listenAddress,
		"expected_pid", expectedPID,
	)
	if phase != PhasePreflight && phase != PhasePostRestart {
		return fmt.Errorf("unknown inference service check phase %q", phase)
	}
	if phase == PhasePostRestart && expectedPID <= 0 {
		return errors.New("post-restart check requires a positive service PID")
	}
	listenerPIDs, err := dependencies.ListenerPIDs(ctx, listenAddress)
	if err != nil {
		slog.ErrorContext(ctx, "inference.service_check.listener_failed", "err", err)
		return fmt.Errorf("inspect configured listener %s: %w", listenAddress, err)
	}
	for _, listenerPID := range listenerPIDs {
		if expectedPID > 0 && listenerPID == expectedPID {
			continue
		}
		return fmt.Errorf(
			"configured listener %s is occupied by non-service PID %d",
			listenAddress,
			listenerPID,
		)
	}
	if phase == PhasePostRestart && !slices.Contains(listenerPIDs, expectedPID) {
		return fmt.Errorf(
			"service PID %d does not own configured listener %s",
			expectedPID,
			listenAddress,
		)
	}
	if phase == PhasePostRestart {
		if err := dependencies.CheckHealth(ctx, listenAddress); err != nil {
			slog.ErrorContext(ctx, "inference.service_check.health_failed", "err", err)
			return fmt.Errorf("inference gRPC health check: %w", err)
		}
	}
	return nil
}

func readListenerPIDs(ctx context.Context, listenAddress string) ([]int, error) {
	_, port, err := net.SplitHostPort(listenAddress)
	if err != nil {
		slog.ErrorContext(ctx, "inference.service_check.address_failed", "err", err)
		return nil, fmt.Errorf("parse listen address: %w", err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return nil, fmt.Errorf("listen address has invalid port %q", port)
	}
	validatedPort := strconv.Itoa(portNumber)
	switch operatingSystem(runtime.GOOS) {
	case operatingSystemDarwin:
		if _, err := exec.LookPath("lsof"); err != nil {
			return nil, errors.New("listener ownership check requires lsof in PATH")
		}
		return readListenerPIDsFromLsof(ctx, validatedPort)
	case operatingSystemLinux:
		if _, err := exec.LookPath("ss"); err != nil {
			return nil, errors.New("listener ownership check requires ss in PATH")
		}
		return readListenerPIDsFromSS(ctx, validatedPort)
	default:
		return nil, fmt.Errorf("listener ownership is unsupported on %s", runtime.GOOS)
	}
}

func readListenerPIDsFromLsof(ctx context.Context, port string) ([]int, error) {
	slog.DebugContext(ctx, "inference.service_check.lsof.begin", "port", port)
	// #nosec G204 -- port is parsed as an integer and rendered canonically by the caller.
	command := exec.CommandContext(
		ctx,
		"lsof",
		"-nP",
		"-t",
		"-iTCP:"+port,
		"-sTCP:LISTEN",
	)
	output, err := command.Output()
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
			return []int{}, nil
		}
		slog.ErrorContext(ctx, "inference.service_check.lsof_failed", "err", err)
		return nil, fmt.Errorf("run lsof: %w", err)
	}
	return readPIDLines(ctx, string(output))
}

func readListenerPIDsFromSS(ctx context.Context, port string) ([]int, error) {
	slog.DebugContext(ctx, "inference.service_check.ss.begin", "port", port)
	// #nosec G204 -- port is parsed as an integer and rendered canonically by the caller.
	command := exec.CommandContext(ctx, "ss", "-H", "-ltnp", "sport = :"+port)
	output, err := command.Output()
	if err != nil {
		slog.ErrorContext(ctx, "inference.service_check.ss_failed", "err", err)
		return nil, fmt.Errorf("run ss: %w", err)
	}
	trimmedOutput := strings.TrimSpace(string(output))
	if trimmedOutput == "" {
		return []int{}, nil
	}
	matches := processIDPattern.FindAllStringSubmatch(trimmedOutput, -1)
	if len(matches) == 0 {
		return nil, errors.New("ss found a listener but did not report its PID")
	}
	processIDs := make([]int, 0, len(matches))
	for _, match := range matches {
		processID, conversionErr := strconv.Atoi(match[1])
		if conversionErr != nil {
			slog.ErrorContext(ctx, "inference.service_check.ss_pid_failed", "err", conversionErr)
			return nil, fmt.Errorf("parse ss PID: %w", conversionErr)
		}
		processIDs = appendUniquePID(processIDs, processID)
	}
	slices.Sort(processIDs)
	return processIDs, nil
}

func readPIDLines(ctx context.Context, output string) ([]int, error) {
	processIDs := make([]int, 0)
	for line := range strings.FieldsSeq(output) {
		processID, err := strconv.Atoi(line)
		if err != nil {
			slog.ErrorContext(ctx, "inference.service_check.pid_failed", "value", line, "err", err)
			return nil, fmt.Errorf("parse listener PID %q: %w", line, err)
		}
		processIDs = appendUniquePID(processIDs, processID)
	}
	slices.Sort(processIDs)
	return processIDs, nil
}

func appendUniquePID(processIDs []int, processID int) []int {
	if slices.Contains(processIDs, processID) {
		return processIDs
	}
	return append(processIDs, processID)
}

func readHealth(ctx context.Context, listenAddress string) error {
	connection, err := grpc.NewClient(
		listenAddress,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		slog.ErrorContext(ctx, "inference.service_check.connect_failed", "err", err)
		return fmt.Errorf("connect: %w", err)
	}
	defer connection.Close()
	client := healthpb.NewHealthClient(connection)
	reply, err := client.Check(ctx, &healthpb.HealthCheckRequest{
		Service: inferencepb.Inference_ServiceDesc.ServiceName,
	})
	if err != nil {
		slog.ErrorContext(ctx, "inference.service_check.rpc_failed", "err", err)
		return fmt.Errorf("check: %w", err)
	}
	if reply.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		return fmt.Errorf("status is %s", reply.GetStatus())
	}
	return nil
}
