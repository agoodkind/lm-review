package main

import (
	"fmt"
	"os/exec"
	"strings"
)

func gitOutput(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func prDiff() (string, error) {
	out, err := exec.Command("git", "diff", "main...HEAD").Output()
	if err != nil {
		out, err = exec.Command("git", "diff", "origin/main...HEAD").Output()
		if err != nil {
			return "", fmt.Errorf("git diff vs main: %w", err)
		}
	}
	return string(out), nil
}

func repoSnapshot() (string, error) {
	out, err := exec.Command("git", "ls-files", "*.go").Output()
	if err != nil {
		return "", fmt.Errorf("git ls-files: %w", err)
	}

	files := strings.Split(strings.TrimSpace(string(out)), "\n")
	var sb strings.Builder
	const maxBytes = 80_000

	for _, f := range files {
		if f == "" {
			continue
		}
		content, readErr := exec.Command("git", "show", "HEAD:"+f).Output()
		if readErr != nil {
			continue
		}
		entry := fmt.Sprintf("// FILE: %s\n%s\n\n", f, content)
		if sb.Len()+len(entry) > maxBytes {
			fmt.Fprintf(&sb, "// ... truncated at %d bytes\n", maxBytes)
			break
		}
		sb.WriteString(entry)
	}

	return sb.String(), nil
}
