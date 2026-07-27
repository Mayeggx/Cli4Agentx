package internal

import (
	"context"
	"fmt"
	"strings"
)

// RegisterSecureCommands exposes a deliberately narrow external-command tool.
// Unlike a shell, it executes one program with explicit arguments and routes
// every invocation through approval, sandbox, timeout, and audit controls.
func RegisterSecureCommands(r *Registry, executor *CommandExecutor) {
	if r == nil || executor == nil {
		return
	}
	r.Register("shell", `Run one approved external executable inside the configured OS sandbox.
  shell <program> [arg...]  — no shell syntax, redirections, or command substitution
Every external command is subject to the command approval policy.`,
		func(args []string, stdin string) (string, error) {
			if len(args) == 0 {
				return "", fmt.Errorf("usage: shell <program> [arg...]")
			}
			result, err := executor.Execute(context.Background(), args[0], args[1:], stdin)
			if err != nil {
				return "", err
			}
			var output strings.Builder
			if result.Stdout != "" {
				output.WriteString(result.Stdout)
			}
			if result.Stderr != "" {
				if output.Len() > 0 && !strings.HasSuffix(output.String(), "\n") {
					output.WriteByte('\n')
				}
				output.WriteString("[stderr]\n")
				output.WriteString(result.Stderr)
			}
			if output.Len() > 0 && !strings.HasSuffix(output.String(), "\n") {
				output.WriteByte('\n')
			}
			fmt.Fprintf(&output, "[command exit:%d | %s]", result.ExitCode, result.Duration)
			return output.String(), nil
		})
}
