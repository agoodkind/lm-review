// Package inferenceservicecheck verifies supervised inference listener handoff.
package inferenceservicecheck

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
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
	PhasePostRestart       = "post-restart"
	maxProcTableBytes      = 16 * 1024 * 1024
	maxProcProcesses       = 1_000_000
	maxProcFileDescriptors = 1_000_000
)

type operatingSystem string

const (
	operatingSystemDarwin operatingSystem = "darwin"
	operatingSystemLinux  operatingSystem = "linux"
)

// Dependencies contains externally observable checks used by Check.
type Dependencies struct {
	ListenerPIDs func(context.Context, string, int) ([]int, error)
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
	listenerPIDs, err := dependencies.ListenerPIDs(ctx, listenAddress, expectedPID)
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

func readListenerPIDs(ctx context.Context, listenAddress string, expectedPID int) ([]int, error) {
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
		return readListenerPIDsFromProc(ctx, "/proc", validatedPort, expectedPID)
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

func readListenerPIDsFromProc(
	ctx context.Context,
	procRoot string,
	port string,
	expectedPID int,
) ([]int, error) {
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return nil, fmt.Errorf("invalid proc listener port %q", port)
	}
	listenerInodes := make(map[uint64]struct{})
	tableCount := 0
	for _, tableName := range []string{"tcp", "tcp6"} {
		tablePath := filepath.Join(procRoot, "net", tableName)
		tableInodes, readErr := readProcTCPTable(ctx, tablePath, uint16(portNumber))
		if readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) {
				continue
			}
			slog.ErrorContext(ctx, "inference.service_check.proc_table_failed", "path", tablePath, "err", readErr)
			return nil, fmt.Errorf("read %s: %w", tablePath, readErr)
		}
		tableCount++
		for inode := range tableInodes {
			listenerInodes[inode] = struct{}{}
		}
	}
	if tableCount == 0 {
		return nil, errors.New("linux listener ownership check found no /proc/net/tcp tables")
	}
	if len(listenerInodes) == 0 {
		return []int{}, nil
	}
	processIDs, err := readProcSocketOwners(ctx, procRoot, listenerInodes, expectedPID)
	if err != nil {
		slog.ErrorContext(ctx, "inference.service_check.proc_owners_failed", "err", err)
		return nil, fmt.Errorf("map listener socket owners: %w", err)
	}
	if len(processIDs) == 0 {
		return nil, errors.New("configured listener exists in /proc but has no owning PID")
	}
	return processIDs, nil
}

func readProcTCPTable(
	ctx context.Context,
	tablePath string,
	port uint16,
) (map[uint64]struct{}, error) {
	file, err := os.Open(tablePath)
	if err != nil {
		slog.ErrorContext(ctx, "inference.service_check.proc_open_failed", "path", tablePath, "err", err)
		return nil, fmt.Errorf("open proc TCP table: %w", err)
	}
	defer file.Close()
	reader := io.LimitReader(file, maxProcTableBytes+1)
	scanner := bufio.NewScanner(reader)
	listenerInodes := make(map[uint64]struct{})
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		if err := ctx.Err(); err != nil {
			slog.ErrorContext(ctx, "inference.service_check.proc_context_done", "err", err)
			return nil, fmt.Errorf("read proc TCP table context: %w", err)
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.Contains(line, "local_address") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 10 {
			return nil, fmt.Errorf("malformed proc TCP row %d", lineNumber)
		}
		_, portText, found := strings.Cut(fields[1], ":")
		if !found || strings.Contains(portText, ":") {
			return nil, fmt.Errorf("malformed proc TCP address on row %d", lineNumber)
		}
		rowPort, conversionErr := strconv.ParseUint(portText, 16, 16)
		if conversionErr != nil {
			slog.ErrorContext(ctx, "inference.service_check.proc_port_failed", "row", lineNumber, "err", conversionErr)
			return nil, fmt.Errorf("parse proc TCP port on row %d: %w", lineNumber, conversionErr)
		}
		if fields[3] != "0A" || uint16(rowPort) != port {
			continue
		}
		inode, conversionErr := strconv.ParseUint(fields[9], 10, 64)
		if conversionErr != nil || inode == 0 {
			return nil, fmt.Errorf("malformed proc TCP inode on row %d", lineNumber)
		}
		listenerInodes[inode] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		slog.ErrorContext(ctx, "inference.service_check.proc_scan_failed", "path", tablePath, "err", err)
		return nil, fmt.Errorf("scan proc TCP table: %w", err)
	}
	if position, err := file.Seek(0, io.SeekCurrent); err == nil && position > maxProcTableBytes {
		return nil, fmt.Errorf("proc TCP table exceeds %d bytes", maxProcTableBytes)
	}
	return listenerInodes, nil
}

func readProcSocketOwners(
	ctx context.Context,
	procRoot string,
	listenerInodes map[uint64]struct{},
	expectedPID int,
) ([]int, error) {
	processIDs, err := readProcProcessIDs(ctx, procRoot, expectedPID)
	if err != nil {
		return nil, err
	}
	ownerPIDs := make([]int, 0)
	foundInodes := make(map[uint64]struct{})
	var permissionErr error
	for _, processID := range processIDs {
		if err := ctx.Err(); err != nil {
			slog.ErrorContext(ctx, "inference.service_check.proc_owner_context_done", "err", err)
			return nil, fmt.Errorf("scan proc socket owners context: %w", err)
		}
		processInodes, processPermissionErr, scanErr := readProcessSocketInodes(
			ctx,
			procRoot,
			processID,
			listenerInodes,
		)
		if scanErr != nil {
			return nil, scanErr
		}
		if permissionErr == nil && processPermissionErr != nil {
			permissionErr = processPermissionErr
		}
		for inode := range processInodes {
			ownerPIDs = appendUniquePID(ownerPIDs, processID)
			foundInodes[inode] = struct{}{}
		}
	}
	if len(foundInodes) != len(listenerInodes) {
		if permissionErr != nil {
			slog.ErrorContext(ctx, "inference.service_check.proc_permission_failed", "err", permissionErr)
			return nil, fmt.Errorf("some listener sockets could not be attributed: %w", permissionErr)
		}
		return nil, errors.New("some listener sockets have no owning PID")
	}
	slices.Sort(ownerPIDs)
	return ownerPIDs, nil
}

func readProcProcessIDs(
	ctx context.Context,
	procRoot string,
	expectedPID int,
) ([]int, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		slog.ErrorContext(ctx, "inference.service_check.proc_root_failed", "path", procRoot, "err", err)
		return nil, fmt.Errorf("read proc root: %w", err)
	}
	processIDs := make([]int, 0)
	for _, entry := range entries {
		processID, conversionErr := strconv.Atoi(entry.Name())
		if conversionErr == nil && processID > 0 && processID != expectedPID {
			processIDs = append(processIDs, processID)
		}
	}
	if len(processIDs) > maxProcProcesses {
		return nil, fmt.Errorf("proc process count exceeds %d", maxProcProcesses)
	}
	slices.Sort(processIDs)
	if expectedPID > 0 {
		processIDs = append([]int{expectedPID}, processIDs...)
	}
	return processIDs, nil
}

func readProcessSocketInodes(
	ctx context.Context,
	procRoot string,
	processID int,
	listenerInodes map[uint64]struct{},
) (map[uint64]struct{}, error, error) {
	fdPath := filepath.Join(procRoot, strconv.Itoa(processID), "fd")
	fileDescriptors, err := os.ReadDir(fdPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[uint64]struct{}{}, nil, nil
		}
		if errors.Is(err, os.ErrPermission) {
			permissionErr := errors.New(
				"read PID " + strconv.Itoa(processID) + " file descriptors: " + err.Error(),
			)
			return map[uint64]struct{}{}, permissionErr, nil
		}
		slog.ErrorContext(ctx, "inference.service_check.proc_fds_failed", "pid", processID, "err", err)
		wrappedErr := fmt.Errorf("read PID %d file descriptors: %w", processID, err)
		return nil, nil, wrappedErr
	}
	if len(fileDescriptors) > maxProcFileDescriptors {
		return nil, nil, fmt.Errorf(
			"pid %d file descriptor count exceeds %d",
			processID,
			maxProcFileDescriptors,
		)
	}
	processInodes := make(map[uint64]struct{})
	var permissionErr error
	for _, fileDescriptor := range fileDescriptors {
		target, linkErr := os.Readlink(filepath.Join(fdPath, fileDescriptor.Name()))
		if linkErr != nil {
			if errors.Is(linkErr, os.ErrNotExist) {
				continue
			}
			if errors.Is(linkErr, os.ErrPermission) {
				if permissionErr == nil {
					permissionErr = errors.New(
						"read PID " + strconv.Itoa(processID) +
							" file descriptor link: " + linkErr.Error(),
					)
				}
				continue
			}
			slog.ErrorContext(ctx, "inference.service_check.proc_fd_link_failed", "pid", processID, "err", linkErr)
			wrappedErr := fmt.Errorf("read PID %d file descriptor link: %w", processID, linkErr)
			return nil, nil, wrappedErr
		}
		inode, found := socketTargetInode(target)
		if !found {
			continue
		}
		if _, found := listenerInodes[inode]; found {
			processInodes[inode] = struct{}{}
		}
	}
	return processInodes, permissionErr, nil
}

func socketTargetInode(target string) (uint64, bool) {
	const prefix = "socket:["
	if !strings.HasPrefix(target, prefix) || !strings.HasSuffix(target, "]") {
		return 0, false
	}
	inodeText := strings.TrimSuffix(strings.TrimPrefix(target, prefix), "]")
	inode, err := strconv.ParseUint(inodeText, 10, 64)
	if err != nil || inode == 0 {
		return 0, false
	}
	return inode, true
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
