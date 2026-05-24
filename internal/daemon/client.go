package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"

	"goodkind.io/gklog"
	"goodkind.io/lm-review/api/reviewpb"
	"goodkind.io/lm-review/internal/xdg"
)

// Client is a gRPC client for the lm-review daemon.
type Client struct {
	conn *grpc.ClientConn
	rpc  reviewpb.LMReviewDClient
}

const (
	daemonDialTimeout  = 2 * time.Second
	daemonStartTimeout = 5 * time.Second
)

// Connect opens a gRPC connection to the running daemon,
// starting it in the background if the socket does not exist yet.
func Connect(ctx context.Context) (*Client, error) {
	log := gklog.LoggerFromContext(ctx).With("component", "lm-review", "subcomponent", "daemon-client")
	socketPath := xdg.DaemonSocketPath()
	started := false

	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		if startErr := startDaemon(); startErr != nil {
			log.ErrorContext(ctx, "daemon.connect.start_failed", "err", startErr)
			return nil, fmt.Errorf("start daemon: %w", startErr)
		}
		started = true
		if waitErr := waitForSocket(socketPath, daemonStartTimeout); waitErr != nil {
			log.ErrorContext(ctx, "daemon.connect.wait_socket_failed", "err", waitErr)
			return nil, fmt.Errorf("daemon did not start: %w", waitErr)
		}
	}

	conn, err := dialDaemon(ctx, socketPath)
	if err != nil && !started {
		log.WarnContext(ctx, "daemon.connect.stale_socket", "socket_path", socketPath, "err", err)
		removeErr := os.Remove(socketPath)
		if removeErr != nil && !os.IsNotExist(removeErr) {
			log.ErrorContext(ctx, "daemon.connect.remove_stale_socket_failed", "err", removeErr)
			return nil, fmt.Errorf("remove stale daemon socket: %w", removeErr)
		}
		startErr := startDaemon()
		if startErr != nil {
			log.ErrorContext(ctx, "daemon.connect.restart_failed", "err", startErr)
			return nil, fmt.Errorf("restart daemon: %w", startErr)
		}
		if waitErr := waitForSocket(socketPath, daemonStartTimeout); waitErr != nil {
			log.ErrorContext(ctx, "daemon.connect.wait_restarted_socket_failed", "err", waitErr)
			return nil, fmt.Errorf("restarted daemon did not start: %w", waitErr)
		}
		conn, err = dialDaemon(ctx, socketPath)
	}
	if err != nil {
		log.ErrorContext(ctx, "daemon.connect.dial_failed", "err", err)
		return nil, fmt.Errorf("connect to daemon: %w", err)
	}

	return &Client{conn: conn, rpc: reviewpb.NewLMReviewDClient(conn)}, nil
}

func dialDaemon(ctx context.Context, socketPath string) (*grpc.ClientConn, error) {
	log := gklog.LoggerFromContext(ctx).With("component", "lm-review", "subcomponent", "daemon-client")
	conn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.ErrorContext(ctx, "daemon.dial.create_client_failed", "err", err)
		return nil, fmt.Errorf("create daemon client: %w", err)
	}

	dialCtx, cancel := context.WithTimeout(ctx, daemonDialTimeout)
	defer cancel()

	conn.Connect()
	for {
		state := conn.GetState()
		if state == connectivity.Ready {
			return conn, nil
		}
		if !conn.WaitForStateChange(dialCtx, state) {
			_ = conn.Close()
			if dialCtx.Err() != nil {
				log.ErrorContext(ctx, "daemon.dial.wait_failed", "err", dialCtx.Err())
				return nil, fmt.Errorf("wait for daemon connection: %w", dialCtx.Err())
			}
			err := fmt.Errorf("daemon connection ended in state %s", state)
			log.ErrorContext(ctx, "daemon.dial.state_failed", "err", err)
			return nil, err
		}
	}
}

// Close closes the connection.
func (c *Client) Close() { _ = c.conn.Close() }

// Review sends a path-based review request to the daemon.
// repoPath is the absolute path to the repo root (for project-local rules).
// depth is "quick", "normal", "deep", or "ultra". model overrides config if non-empty.
func (c *Client) Review(ctx context.Context, mode reviewpb.ReviewMode, repoPath string, depth string, model string, baseRef string) (*reviewpb.ReviewResponse, error) {
	log := gklog.LoggerFromContext(ctx).With("component", "lm-review", "subcomponent", "daemon-client")
	resp, err := c.rpc.Review(ctx, &reviewpb.ReviewRequest{
		Mode:    mode,
		Path:    repoPath,
		Depth:   depth,
		Model:   model,
		BaseRef: baseRef,
	})
	if err != nil {
		log.ErrorContext(ctx, "daemon.client.review_failed", "err", err)
		return nil, fmt.Errorf("review RPC: %w", err)
	}
	return resp, nil
}

// ReviewStatic sends a static review request to the daemon.
func (c *Client) ReviewStatic(ctx context.Context, req *reviewpb.StaticReviewRequest) (*reviewpb.ReviewResponse, error) {
	return c.rpc.ReviewStatic(ctx, req)
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
