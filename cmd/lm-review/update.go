package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"goodkind.io/go-makefile/selfupdate"
	"goodkind.io/lm-review/internal/updateopts"
)

// newUpdateCmd builds the update command tree. lm-review runs as a
// per-invocation CLI and MCP process with an on-demand daemon, so apply only
// replaces the on-disk binary; the next process launch picks it up. There is
// no scheduler and no managed service to restart.
func newUpdateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Check, apply, or show release update state",
	}
	cmd.AddCommand(newUpdateCheckCmd())
	cmd.AddCommand(newUpdateApplyCmd())
	cmd.AddCommand(newUpdateStatusCmd())
	return cmd
}

func newUpdateCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Check whether a newer release is available",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			log := lmReviewLog(ctx)
			result, err := selfupdate.Check(ctx, updateopts.Options(updateopts.Overrides{
				Client:      nil,
				InstallPath: "",
				DryRun:      false,
				Log:         log,
			}))
			if err != nil {
				log.ErrorContext(ctx, "lm-review.update.check_failed", "err", err)
				return fmt.Errorf("update check: %w", err)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "current version: "+result.CurrentVersion)
			fmt.Fprintln(out, "latest tag:      "+result.LatestTag)
			fmt.Fprintln(out, "asset:           "+result.AssetName)
			if result.UpdateAvailable {
				fmt.Fprintln(out, "update available: yes")
			} else {
				fmt.Fprintln(out, "update available: no")
			}
			return nil
		},
	}
}

func newUpdateApplyCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Download, verify, and install the latest release",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			log := lmReviewLog(ctx)
			result, err := selfupdate.Apply(ctx, updateopts.Options(updateopts.Overrides{
				Client:      nil,
				InstallPath: "",
				DryRun:      dryRun,
				Log:         log,
			}))
			if err != nil {
				log.ErrorContext(ctx, "lm-review.update.apply_failed", "err", err)
				return fmt.Errorf("update apply: %w", err)
			}
			out := cmd.OutOrStdout()
			switch {
			case !result.UpdateAvailable:
				fmt.Fprintln(out, "lm-review: already current")
			case result.DryRun:
				fmt.Fprintln(out, "lm-review: update apply dry run ok")
			case !result.Applied:
				fmt.Fprintln(out, "lm-review: update available but not applied")
			default:
				fmt.Fprintln(out, "lm-review: update applied; a running daemon uses the new binary after it next restarts")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "download and verify without installing")
	return cmd
}

func newUpdateStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the last-known update state",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			log := lmReviewLog(ctx)
			options := updateopts.Options(updateopts.Overrides{
				Client:      nil,
				InstallPath: "",
				DryRun:      false,
				Log:         log,
			})
			state, err := selfupdate.LoadState(options.StatePath)
			if err != nil {
				log.ErrorContext(ctx, "lm-review.update.status_failed", "err", err, "path", options.StatePath)
				return fmt.Errorf("update status: %w", err)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "current version:   "+options.Config.CurrentVersion)
			fmt.Fprintln(out, "current commit:    "+options.Config.CurrentCommit)
			fmt.Fprintln(out, "current buildHash: "+options.Config.CurrentBuildHash)
			if !state.LastCheckAt.IsZero() {
				fmt.Fprintln(out, "last check:        "+state.LastCheckAt.Format(time.RFC3339))
			}
			if !state.NextCheckAt.IsZero() {
				fmt.Fprintln(out, "next check:        "+state.NextCheckAt.Format(time.RFC3339))
			}
			if state.LatestTag != "" {
				fmt.Fprintln(out, "latest tag:        "+state.LatestTag)
			}
			if state.AppliedTag != "" {
				fmt.Fprintln(out, "applied tag:       "+state.AppliedTag)
			}
			if state.LastResult != "" {
				fmt.Fprintln(out, "last result:       "+state.LastResult)
			}
			if state.LastError != "" {
				fmt.Fprintln(out, "last error:        "+state.LastError)
			}
			return nil
		},
	}
}
