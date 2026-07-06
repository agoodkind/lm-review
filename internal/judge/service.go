package judge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"goodkind.io/lm-review/api/judgepb"
)

// DecideFunc lets tests replace the model call while production uses Decide.
type DecideFunc func(context.Context, string, string, string) (judgepb.Verdict, error)

// Server implements the judge.v1 Judge gRPC service.
type Server struct {
	judgepb.UnimplementedJudgeServer
	model   string
	rules   map[string]RuleSet
	decider DecideFunc
}

// NewServer returns a judge gRPC server backed by the default model decider.
func NewServer(model string) *Server {
	return NewServerWithDecider(model, Decide)
}

// NewServerWithDecider returns a judge gRPC server backed by an injected decider.
func NewServerWithDecider(model string, decider DecideFunc) *Server {
	if decider == nil {
		decider = Decide
	}
	return &Server{
		UnimplementedJudgeServer: judgepb.UnimplementedJudgeServer{},
		model:                    model,
		rules:                    Registry(),
		decider:                  decider,
	}
}

// Evaluate renders the selected rule-set prompt and returns the model verdict.
func (s *Server) Evaluate(ctx context.Context, req *judgepb.JudgeRequest) (*judgepb.JudgeReply, error) {
	ruleSetID := strings.TrimSpace(req.GetRuleSetId())
	ruleSet, ok := s.rules[ruleSetID]
	if !ok {
		return nil, status.Errorf(codes.InvalidArgument, "unknown rule_set_id %q", ruleSetID)
	}

	localContext := ruleContextFromProto(req.GetContext())
	systemPrompt := ruleSet.System(localContext)
	userPrompt := ruleSet.User(req.GetInputText(), localContext)
	verdict, err := s.decider(ctx, s.model, systemPrompt, userPrompt)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "judge model: %v", err)
	}
	return &judgepb.JudgeReply{Verdict: verdict}, nil
}

// ListRuleSets returns the supported rule-set ids and their required context fields.
func (s *Server) ListRuleSets(context.Context, *judgepb.ListRuleSetsRequest) (*judgepb.ListRuleSetsReply, error) {
	ids := make([]string, 0, len(s.rules))
	for id := range s.rules {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	reply := &judgepb.ListRuleSetsReply{}
	for _, id := range ids {
		ruleSet := s.rules[id]
		reply.RuleSets = append(reply.RuleSets, &judgepb.RuleSetDescriptor{
			Id:              ruleSet.ID,
			RequiredContext: append([]string{}, ruleSet.RequiredContext...),
		})
	}
	return reply, nil
}

// Serve starts the judge gRPC service on the supplied listen address.
func Serve(ctx context.Context, listenAddress string, model string) error {
	listenConfig := &net.ListenConfig{}
	listener, err := listenConfig.Listen(ctx, "tcp", listenAddress)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", listenAddress, err)
	}

	grpcServer := grpc.NewServer()
	judgepb.RegisterJudgeServer(grpcServer, NewServer(model))
	stopped := make(chan struct{})
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(ctx, "judge.serve.stop_panic", "err", fmt.Errorf("panic: %v", recovered))
			}
		}()
		select {
		case <-ctx.Done():
			grpcServer.GracefulStop()
		case <-stopped:
		}
	}()
	defer close(stopped)

	if err := grpcServer.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		return fmt.Errorf("serve judge gRPC: %w", err)
	}
	return nil
}

func ruleContextFromProto(ctx *judgepb.RuleContext) RuleContext {
	if ctx == nil {
		return RuleContext{
			Conversation: nil,
			IndexedRoots: nil,
			Worktree: WorktreeState{
				PrimaryCheckout: "",
				DefaultBranch:   "",
				Worktrees:       nil,
				CurrentWorktree: "",
				CurrentBranch:   "",
			},
			CWD:        "",
			CWDIndexed: "",
		}
	}
	return RuleContext{
		Conversation: conversationFromProto(ctx.GetConversation()),
		IndexedRoots: append([]string{}, ctx.GetIndexedRoots()...),
		Worktree:     worktreeStateFromProto(ctx.GetWorktree()),
		CWD:          ctx.GetCwd(),
		CWDIndexed:   ctx.GetCwdIndexed(),
	}
}

func conversationFromProto(turns []*judgepb.ConversationTurn) []ConversationTurn {
	conversation := make([]ConversationTurn, 0, len(turns))
	for _, turn := range turns {
		conversation = append(conversation, ConversationTurn{
			Role: turn.GetRole(),
			Text: turn.GetText(),
		})
	}
	return conversation
}

func worktreeStateFromProto(state *judgepb.WorktreeState) WorktreeState {
	if state == nil {
		return WorktreeState{
			PrimaryCheckout: "",
			DefaultBranch:   "",
			Worktrees:       nil,
			CurrentWorktree: "",
			CurrentBranch:   "",
		}
	}
	worktrees := make([]Worktree, 0, len(state.GetWorktrees()))
	for _, worktree := range state.GetWorktrees() {
		worktrees = append(worktrees, Worktree{
			Path:      worktree.GetPath(),
			Branch:    worktree.GetBranch(),
			IsPrimary: worktree.GetIsPrimary(),
		})
	}
	return WorktreeState{
		PrimaryCheckout: state.GetPrimaryCheckout(),
		DefaultBranch:   state.GetDefaultBranch(),
		Worktrees:       worktrees,
		CurrentWorktree: state.GetCurrentWorktree(),
		CurrentBranch:   state.GetCurrentBranch(),
	}
}
