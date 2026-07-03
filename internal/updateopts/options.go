// Package updateopts adapts lm-review build metadata to selfupdate options.
package updateopts

import (
	"log/slog"
	"net/http"

	"goodkind.io/go-makefile/selfupdate"
	"goodkind.io/lm-review/internal/version"
)

const (
	updateRepo   = "agoodkind/lm-review"
	updateBinary = "lm-review"
)

// Overrides carries operation-specific update settings.
type Overrides struct {
	Client      *http.Client
	InstallPath string
	DryRun      bool
	Log         *slog.Logger
}

// Options builds selfupdate options using the library default state and cache
// paths because lm-review has no repo-specific update state convention.
func Options(overrides Overrides) selfupdate.Options {
	return selfupdate.Options{
		Config: selfupdate.Config{
			Repo:             updateRepo,
			Binary:           updateBinary,
			CurrentVersion:   version.Version,
			CurrentCommit:    version.Commit,
			CurrentBuildHash: version.BuildHash(),
			AllowPrerelease:  nil,
			ValidateArgs:     []string{"version"},
			ValidateMatch:    "lm-review ",
		},
		Client:      overrides.Client,
		InstallPath: overrides.InstallPath,
		CacheDir:    selfupdate.DefaultCacheDir(updateBinary),
		StatePath:   selfupdate.DefaultStatePath(updateBinary),
		DryRun:      overrides.DryRun,
		Log:         overrides.Log,
	}
}
