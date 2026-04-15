package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"goodkind.io/lm-review/api/reviewpb"
	"goodkind.io/lm-review/internal/xdg"
)

// Client is a gRPC client for the lm-review daemon.
type Client struct {
	conn *grpc.ClientConn
	rpc  reviewpb.LMReviewDClient
}

// Connect opens a gRPC connection to the running daemon,
// starting it in the background if the socket does not exist yet.
func Connect(ctx context.Context) (*Client, error) {
	socketPath := xdg.DaemonSocketPath()

	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		if startErr := startDaemon(); startErr != nil {
			return nil, fmt.Errorf("start daemon: %w", startErr)
		}
		if waitErr := waitForSocket(socketPath, 5*time.Second); waitErr != nil {
			return nil, fmt.Errorf("daemon did not start: %w", waitErr)
		}
	}

	conn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("connect to daemon: %w", err)
	}

	return &Client{conn: conn, rpc: reviewpb.NewLMReviewDClient(conn)}, nil
}

// Close closes the connection.
func (c *Client) Close() { _ = c.conn.Close() }

// ReviewDiff sends a diff review request to the daemon.
// repoPath is the absolute path to the repo root (for project-local rules).
// depth is "quick", "normal", "deep", or "ultra". model overrides config if non-empty.
func (c *Client) ReviewDiff(ctx context.Context, diff, repoPath string, depth string, model string) (*reviewpb.ReviewResponse, error) {
	return c.rpc.ReviewDiff(ctx, &reviewpb.ReviewRequest{Diff: diff, Path: repoPath, Depth: depth, Model: model})
}

// ReviewPR sends a PR diff review request to the daemon.
// repoPath is the absolute path to the repo root (for project-local rules).
// depth is "quick", "normal", "deep", or "ultra". model overrides config if non-empty.
func (c *Client) ReviewPR(ctx context.Context, diff, repoPath string, depth string, model string) (*reviewpb.ReviewResponse, error) {
	return c.rpc.ReviewPR(ctx, &reviewpb.ReviewRequest{Diff: diff, Path: repoPath, Depth: depth, Model: model})
}

// ReviewRepo sends a full repo review request to the daemon.
// repoPath is the absolute path to the repo root (for project-local rules).
// depth is "quick", "normal", "deep", or "ultra". model overrides config if non-empty.
func (c *Client) ReviewRepo(ctx context.Context, files, repoPath string, depth string, model string) (*reviewpb.ReviewResponse, error) {
	return c.rpc.ReviewRepo(ctx, &reviewpb.ReviewRequest{Diff: files, Path: repoPath, Depth: depth, Model: model})
}

func startDaemon() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}

	cmd := exec.Command(self, "daemon")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return err
	}

	go func() { _ = cmd.Wait() }()
	return nil
}

func waitForSocket(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for socket %s", path)
}
