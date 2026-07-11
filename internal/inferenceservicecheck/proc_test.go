package inferenceservicecheck

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const procTCPHeader = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"

func TestReadListenerPIDsFromProcMatchesConfiguredIPv4Loopback(t *testing.T) {
	procRoot := t.TempDir()
	writeProcTable(t, procRoot, "tcp", procTCPLine("0100007F", "1519", "0A", 4101))
	writeProcTable(t, procRoot, "tcp6", procTCPLine(strings.Repeat("0", 31)+"1", "1519", "0A", 4102))
	writeProcSocketOwner(t, procRoot, 41, 4101)

	processIDs, err := readListenerPIDsFromProc(context.Background(), procRoot, "5401", 41)
	if err != nil {
		t.Fatalf("readListenerPIDsFromProc returned error: %v", err)
	}
	if len(processIDs) != 1 || processIDs[0] != 41 {
		t.Fatalf("process IDs=%v, want [41]", processIDs)
	}
}

func TestReadListenerPIDsFromProcMatchesConfiguredIPv6Loopback(t *testing.T) {
	procRoot := t.TempDir()
	writeProcTable(t, procRoot, "tcp", procTCPLine("0100007F", "1519", "0A", 4101))
	writeProcTable(t, procRoot, "tcp6", procTCPLine("00000000000000000000000001000000", "1519", "0A", 4102))
	writeProcSocketOwner(t, procRoot, 82, 4102)

	processIDs, err := readListenerPIDsFromProcAddress(
		context.Background(), procRoot, netip.MustParseAddr("::1"), "5401", 82,
	)
	if err != nil {
		t.Fatalf("readListenerPIDsFromProcAddress returned error: %v", err)
	}
	if len(processIDs) != 1 || processIDs[0] != 82 {
		t.Fatalf("process IDs=%v, want [82]", processIDs)
	}
}

func TestReadListenerPIDsFromProcIgnoresOtherIPv4Address(t *testing.T) {
	procRoot := t.TempDir()
	lines := procTCPLine("0200007F", "1519", "0A", 4101) +
		procTCPLine("0100007F", "1519", "0A", 4102)
	writeProcTable(t, procRoot, "tcp", lines)
	writeProcSocketOwner(t, procRoot, 41, 4101)
	writeProcSocketOwner(t, procRoot, 82, 4102)

	processIDs, err := readListenerPIDsFromProc(context.Background(), procRoot, "5401", 82)
	if err != nil {
		t.Fatalf("readListenerPIDsFromProc returned error: %v", err)
	}
	if len(processIDs) != 1 || processIDs[0] != 82 {
		t.Fatalf("process IDs=%v, want [82]", processIDs)
	}
}

func TestReadListenerPIDsFromProcMatchesIPv4WildcardOnly(t *testing.T) {
	procRoot := t.TempDir()
	lines := procTCPLine("00000000", "1519", "0A", 4101) +
		procTCPLine("0100007F", "1519", "0A", 4102)
	writeProcTable(t, procRoot, "tcp", lines)
	writeProcSocketOwner(t, procRoot, 41, 4101)
	writeProcSocketOwner(t, procRoot, 82, 4102)

	processIDs, err := readListenerPIDsFromProcAddress(
		context.Background(), procRoot, netip.MustParseAddr("0.0.0.0"), "5401", 41,
	)
	if err != nil {
		t.Fatalf("readListenerPIDsFromProcAddress returned error: %v", err)
	}
	if len(processIDs) != 1 || processIDs[0] != 41 {
		t.Fatalf("process IDs=%v, want [41]", processIDs)
	}
}

func TestReadListenerPIDsFromProcFindsExpectedOwner(t *testing.T) {
	procRoot := t.TempDir()
	writeProcTable(t, procRoot, "tcp", procTCPLine("0100007F", "1519", "0A", 4101))
	writeProcTable(t, procRoot, "tcp6", "")
	writeProcSocketOwner(t, procRoot, 41, 4101)

	processIDs, err := readListenerPIDsFromProc(context.Background(), procRoot, "5401", 41)
	if err != nil {
		t.Fatalf("readListenerPIDsFromProc returned error: %v", err)
	}
	if len(processIDs) != 1 || processIDs[0] != 41 {
		t.Fatalf("process IDs=%v, want [41]", processIDs)
	}
}

func TestReadListenerPIDsFromProcFindsForeignOwner(t *testing.T) {
	procRoot := t.TempDir()
	writeProcTable(t, procRoot, "tcp", procTCPLine("0100007F", "1519", "0A", 4101))
	writeProcTable(t, procRoot, "tcp6", "")
	writeProcSocketOwner(t, procRoot, 82, 4101)

	processIDs, err := readListenerPIDsFromProc(context.Background(), procRoot, "5401", 41)
	if err != nil {
		t.Fatalf("readListenerPIDsFromProc returned error: %v", err)
	}
	if len(processIDs) != 1 || processIDs[0] != 82 {
		t.Fatalf("process IDs=%v, want [82]", processIDs)
	}
}

func TestReadListenerPIDsFromProcIgnoresOtherPortsAndStates(t *testing.T) {
	procRoot := t.TempDir()
	lines := procTCPLine("0100007F", "151A", "0A", 4101) +
		procTCPLine("0100007F", "1519", "01", 4102)
	writeProcTable(t, procRoot, "tcp", lines)
	writeProcTable(t, procRoot, "tcp6", "")

	processIDs, err := readListenerPIDsFromProc(context.Background(), procRoot, "5401", 0)
	if err != nil {
		t.Fatalf("readListenerPIDsFromProc returned error: %v", err)
	}
	if len(processIDs) != 0 {
		t.Fatalf("process IDs=%v, want none", processIDs)
	}
}

func TestReadListenerPIDsFromProcRejectsOwnerlessListener(t *testing.T) {
	procRoot := t.TempDir()
	writeProcTable(t, procRoot, "tcp", procTCPLine("0100007F", "1519", "0A", 4101))
	writeProcTable(t, procRoot, "tcp6", "")

	_, err := readListenerPIDsFromProc(context.Background(), procRoot, "5401", 0)
	if err == nil || !strings.Contains(err.Error(), "no owning PID") {
		t.Fatalf("error=%v, want ownerless listener detail", err)
	}
}

func TestReadListenerPIDsFromProcRejectsMalformedTable(t *testing.T) {
	procRoot := t.TempDir()
	writeProcTable(t, procRoot, "tcp", "malformed\n")
	writeProcTable(t, procRoot, "tcp6", "")

	_, err := readListenerPIDsFromProc(context.Background(), procRoot, "5401", 0)
	if err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("error=%v, want malformed table detail", err)
	}
}

func TestReadListenerPIDsFromProcReturnsPermissionFailure(t *testing.T) {
	procRoot := t.TempDir()
	writeProcTable(t, procRoot, "tcp", procTCPLine("0100007F", "1519", "0A", 4101))
	writeProcTable(t, procRoot, "tcp6", "")
	fdPath := filepath.Join(procRoot, "41", "fd")
	if err := os.MkdirAll(fdPath, 0o700); err != nil {
		t.Fatalf("create fd directory: %v", err)
	}
	if err := os.Chmod(fdPath, 0); err != nil {
		t.Fatalf("remove fd permissions: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(fdPath, 0o700)
	})

	_, err := readListenerPIDsFromProc(context.Background(), procRoot, "5401", 41)
	if err == nil || !strings.Contains(err.Error(), "read PID 41 file descriptors") {
		t.Fatalf("error=%v, want permission detail", err)
	}
}

func TestReadListenerPIDsFromProcIgnoresUnrelatedPermissionFailure(t *testing.T) {
	procRoot := t.TempDir()
	writeProcTable(t, procRoot, "tcp", procTCPLine("0100007F", "1519", "0A", 4101))
	writeProcTable(t, procRoot, "tcp6", "")
	writeProcSocketOwner(t, procRoot, 41, 4101)
	fdPath := filepath.Join(procRoot, "82", "fd")
	if err := os.MkdirAll(fdPath, 0o700); err != nil {
		t.Fatalf("create unrelated fd directory: %v", err)
	}
	if err := os.Chmod(fdPath, 0); err != nil {
		t.Fatalf("remove unrelated fd permissions: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(fdPath, 0o700)
	})

	processIDs, err := readListenerPIDsFromProc(context.Background(), procRoot, "5401", 41)
	if err != nil {
		t.Fatalf("readListenerPIDsFromProc returned error: %v", err)
	}
	if len(processIDs) != 1 || processIDs[0] != 41 {
		t.Fatalf("process IDs=%v, want [41]", processIDs)
	}
}

func writeProcTable(t *testing.T, procRoot string, name string, lines string) {
	t.Helper()
	path := filepath.Join(procRoot, "net", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create proc net: %v", err)
	}
	if err := os.WriteFile(path, []byte(procTCPHeader+lines), 0o600); err != nil {
		t.Fatalf("write proc table: %v", err)
	}
}

func writeProcSocketOwner(t *testing.T, procRoot string, processID int, inode uint64) {
	t.Helper()
	fdPath := filepath.Join(procRoot, fmt.Sprintf("%d", processID), "fd")
	if err := os.MkdirAll(fdPath, 0o700); err != nil {
		t.Fatalf("create fd directory: %v", err)
	}
	linkPath := filepath.Join(fdPath, fmt.Sprintf("%d", inode))
	if err := os.Symlink(fmt.Sprintf("socket:[%d]", inode), linkPath); err != nil {
		t.Fatalf("create socket symlink: %v", err)
	}
}

func procTCPLine(address string, port string, state string, inode uint64) string {
	return fmt.Sprintf(
		"   0: %s:%s 00000000:0000 %s 00000000:00000000 00:00000000 00000000  1000 0 %d\n",
		address,
		port,
		state,
		inode,
	)
}

func readListenerPIDsFromProc(
	ctx context.Context,
	procRoot string,
	port string,
	expectedPID int,
) ([]int, error) {
	return readListenerPIDsFromProcAddress(
		ctx,
		procRoot,
		netip.MustParseAddr("127.0.0.1"),
		port,
		expectedPID,
	)
}
