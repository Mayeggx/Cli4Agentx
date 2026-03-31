package internal

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

func RegisterTeamCommands(r *Registry, db *sql.DB, teamRunID, currentRole string) {
	r.Register("team", `Interact with the current team run.
  team status                       — show current stage and role status
  team send <role> <message>        — send a message to another role
  team broadcast <message>          — send a message to all other roles
  team messages [limit]             — show recent team messages
  team artifact <role>              — show the shared artifact path for a role
`, func(args []string, stdin string) (string, error) {
		if len(args) == 0 {
			return "", fmt.Errorf("usage: team status|send|broadcast|messages|artifact ...")
		}
		switch args[0] {
		case "status":
			state, err := LoadTeamRunState(teamRunID)
			if err != nil {
				return "", err
			}
			return FormatTeamStatus(state, 5), nil
		case "send":
			if len(args) < 3 {
				return "", fmt.Errorf("usage: team send <role> <message>")
			}
			content := strings.TrimSpace(strings.Join(args[2:], " "))
			if content == "" && stdin != "" {
				content = strings.TrimSpace(stdin)
			}
			if content == "" {
				return "", fmt.Errorf("message is required")
			}
			msgs, err := SendTeamMessages(db, teamRunID, currentRole, args[1], content)
			if err != nil {
				return "", err
			}
			return formatSentMessages(msgs), nil
		case "broadcast":
			content := strings.TrimSpace(strings.Join(args[1:], " "))
			if content == "" && stdin != "" {
				content = strings.TrimSpace(stdin)
			}
			if content == "" {
				return "", fmt.Errorf("usage: team broadcast <message>")
			}
			msgs, err := SendTeamMessages(db, teamRunID, currentRole, "*", content)
			if err != nil {
				return "", err
			}
			return formatSentMessages(msgs), nil
		case "messages":
			limit := 10
			if len(args) > 1 {
				fmt.Sscanf(args[1], "%d", &limit)
			}
			msgs, err := ListTeamMessages(teamRunID, limit)
			if err != nil {
				return "", err
			}
			return formatTeamMessages(msgs), nil
		case "artifact":
			if len(args) < 2 {
				return "", fmt.Errorf("usage: team artifact <role>")
			}
			state, err := LoadTeamRunState(teamRunID)
			if err != nil {
				return "", err
			}
			role := teamRoleByName(state, args[1])
			if role == nil {
				return "", fmt.Errorf("unknown role %q", args[1])
			}
			return fmt.Sprintf("/%s/%s", state.CoordinatorTopicID, role.Artifact), nil
		default:
			return "", fmt.Errorf("unknown: team %s. Use: status|send|broadcast|messages|artifact", args[0])
		}
	})
}

func FormatTeamStatus(state *TeamRunState, recentMessageLimit int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Team run: %s\n", state.ID)
	fmt.Fprintf(&b, "Status: %s\n", state.Status)
	fmt.Fprintf(&b, "Current stage: %d\n", state.CurrentStage)
	fmt.Fprintf(&b, "Coordinator topic: %s\n", state.CoordinatorTopicID)

	stageIndexes := collectStageIndexes(state)
	b.WriteString("\nStages:\n")
	for _, stageIndex := range stageIndexes {
		roles := rolesForStage(state, stageIndex)
		doneCount := 0
		runningCount := 0
		errorCount := 0
		for _, role := range roles {
			switch role.Status {
			case "done":
				doneCount++
			case "running":
				runningCount++
			case "error", "cancelled":
				errorCount++
			}
		}
		fmt.Fprintf(&b, "- stage %d: %d total, %d done, %d running, %d error\n", stageIndex, len(roles), doneCount, runningCount, errorCount)
		for _, role := range roles {
			fmt.Fprintf(&b, "  [%s] %s (topic %s", role.Status, role.Name, role.TopicID)
			if role.RunID != "" {
				fmt.Fprintf(&b, ", run %s", role.RunID)
			}
			b.WriteString(")\n")
		}
	}

	recent := state.Messages
	if recentMessageLimit > 0 && len(recent) > recentMessageLimit {
		recent = recent[len(recent)-recentMessageLimit:]
	}
	if len(recent) > 0 {
		b.WriteString("\nRecent messages:\n")
		for _, msg := range recent {
			ts := time.Unix(msg.CreatedAt, 0).Format("15:04:05")
			from := msg.FromRole
			if strings.TrimSpace(from) == "" {
				from = "system"
			}
			fmt.Fprintf(&b, "- [%s] %s -> %s [%s] %s\n", ts, from, msg.ToRole, msg.DeliveryStatus, truncateTeamText(msg.Content, 80))
		}
	}
	return b.String()
}

func formatSentMessages(messages []TeamMessage) string {
	var b strings.Builder
	for _, msg := range messages {
		fmt.Fprintf(&b, "sent to %s [%s]\n", msg.ToRole, msg.DeliveryStatus)
	}
	return strings.TrimSpace(b.String())
}

func formatTeamMessages(messages []TeamMessage) string {
	if len(messages) == 0 {
		return "No team messages yet."
	}
	var b strings.Builder
	for _, msg := range messages {
		ts := time.Unix(msg.CreatedAt, 0).Format("15:04:05")
		from := msg.FromRole
		if strings.TrimSpace(from) == "" {
			from = "system"
		}
		fmt.Fprintf(&b, "[%s] %s -> %s [%s]\n%s\n", ts, from, msg.ToRole, msg.DeliveryStatus, msg.Content)
	}
	return strings.TrimSpace(b.String())
}

func collectStageIndexes(state *TeamRunState) []int {
	stageSet := map[int]bool{}
	for _, role := range state.Roles {
		stageSet[role.Stage] = true
	}
	stageIndexes := make([]int, 0, len(stageSet))
	for stage := range stageSet {
		stageIndexes = append(stageIndexes, stage)
	}
	sort.Ints(stageIndexes)
	return stageIndexes
}

func rolesForStage(state *TeamRunState, stage int) []TeamRoleState {
	roles := make([]TeamRoleState, 0)
	for _, role := range state.Roles {
		if role.Stage == stage {
			roles = append(roles, role)
		}
	}
	return roles
}

func truncateTeamText(s string, max int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max]) + "..."
}
