package internal

import "fmt"

// RegisterReadOnlyConfigCommands keeps configuration visible to the agent while
// preventing tool calls from changing future process security policy.
func RegisterReadOnlyConfigCommands(r *Registry) {
	if r == nil {
		return
	}
	r.Register("config", `View the current agent configuration.
  config — show current config
Configuration changes must be made directly by the user outside an agent run.`,
		func(args []string, _ string) (string, error) {
			if len(args) != 0 {
				return "", fmt.Errorf("config is read-only during an agent run")
			}
			cfg, err := LoadConfig()
			if err != nil {
				return "", err
			}
			return ConfigGetText(cfg), nil
		})
}
