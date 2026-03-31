package internal

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Summary remains for compatibility with topic/run views.
type Summary struct {
	ID          int     `json:"id"`
	TopicID     string  `json:"topic_id"`
	TopicName   string  `json:"topic_name,omitempty"`
	RunID       string  `json:"run_id,omitempty"`
	SummaryText string  `json:"summary"`
	UserMessage string  `json:"user_message"`
	Similarity  float32 `json:"similarity,omitempty"`
	CreatedAt   int64   `json:"created_at"`
}

func StoreSummary(db *sql.DB, topicID, runID, summary, userMessage string, embedding []float32, embeddingModel string) error {
	return SyncLocalMemoryStore(db, topicID, runID, userMessage, summary)
}

func GetRecentSummaries(db *sql.DB, limit int) ([]string, error) {
	return RecentRunSummaries(limit), nil
}

type SearchFilter struct {
	TopicID string
	Keyword string
	Limit   int
}

func SearchMemory(db *sql.DB, cfg *Config, query string, filter SearchFilter) ([]Summary, error) {
	if filter.Limit == 0 {
		filter.Limit = 5
	}
	hits, err := SearchAllMemory(db, cfg, query, filter)
	if err != nil {
		return nil, err
	}
	results := make([]Summary, 0, len(hits))
	for _, hit := range hits {
		results = append(results, Summary{
			TopicID:     extractTopicID(hit),
			RunID:       extractRunID(hit),
			SummaryText: hit.Text,
			Similarity:  float32(minFloat(hit.Score/2.0, 0.99)),
			CreatedAt:   hit.CreatedAt,
		})
	}
	enrichTopicNames(db, results)
	return results, nil
}

func enrichTopicNames(db *sql.DB, summaries []Summary) {
	cache := make(map[string]string)
	for i := range summaries {
		tid := summaries[i].TopicID
		if tid == "" {
			continue
		}
		if name, ok := cache[tid]; ok {
			summaries[i].TopicName = name
			continue
		}
		var name string
		if err := db.QueryRow(`SELECT name FROM topics WHERE id = ?`, tid).Scan(&name); err == nil {
			cache[tid] = name
			summaries[i].TopicName = name
		}
	}
}

func FormatSearchResults(results []Summary) string {
	if len(results) == 0 {
		return "No matching memories found."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d memory results:\n", len(results))
	for _, r := range results {
		ts := time.Unix(r.CreatedAt, 0).Format("01-02 15:04")
		sim := ""
		if r.Similarity > 0 {
			sim = fmt.Sprintf(" (%.0f%%)", r.Similarity*100)
		}
		topicLabel := r.TopicID
		if len(topicLabel) > 8 {
			topicLabel = topicLabel[:8]
		}
		if r.TopicName != "" {
			topicLabel = r.TopicName
		}
		fmt.Fprintf(&b, "  [%s]%s topic=%q", ts, sim, topicLabel)
		if r.RunID != "" {
			fmt.Fprintf(&b, " run=%s", r.RunID)
		}
		fmt.Fprintf(&b, "\n    %s\n", r.SummaryText)
	}
	return b.String()
}

func SearchAllMemory(db *sql.DB, cfg *Config, query string, filter SearchFilter) ([]MemoryHit, error) {
	limit := filter.Limit
	if limit == 0 {
		limit = 5
	}

	results, err := SearchLocalMemory(query, limit*4)
	if err != nil {
		return nil, err
	}

	var filtered []MemoryHit
	for _, hit := range results {
		if filter.TopicID != "" && !memoryHitMatchesTopic(hit, filter.TopicID) {
			continue
		}
		hit.Score = adjustMemoryHitScore(query, hit)
		if hit.Score <= 0 {
			continue
		}
		filtered = append(filtered, hit)
	}

	if filter.Keyword != "" {
		kw := strings.ToLower(filter.Keyword)
		var kwFiltered []MemoryHit
		for _, hit := range filtered {
			if strings.Contains(strings.ToLower(hit.Text), kw) {
				kwFiltered = append(kwFiltered, hit)
			}
		}
		filtered = kwFiltered
	}
	if !isMetaMemoryQuery(query) {
		var nonMeta []MemoryHit
		for _, hit := range filtered {
			if isMetaMemoryText(hit.Text) {
				continue
			}
			nonMeta = append(nonMeta, hit)
		}
		if len(nonMeta) > 0 {
			filtered = nonMeta
		}
	}

	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].Score == filtered[j].Score {
			return filtered[i].CreatedAt > filtered[j].CreatedAt
		}
		return filtered[i].Score > filtered[j].Score
	})

	seen := make(map[string]bool)
	var deduped []MemoryHit
	for _, hit := range filtered {
		key := hit.Layer + "|" + hit.Text
		if seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, hit)
		if len(deduped) >= limit {
			break
		}
	}
	return deduped, nil
}

func RecallMemories(db *sql.DB, cfg *Config, topicID, query string, limit int) ([]MemoryHit, error) {
	if limit <= 0 {
		limit = 3
	}
	results, err := SearchAllMemory(db, cfg, query, SearchFilter{TopicID: topicID, Limit: limit})
	if err != nil {
		return nil, err
	}
	var durable []MemoryHit
	for _, hit := range results {
		if isDurableRecallLayer(hit.Layer) || hit.Layer == "P2" {
			durable = append(durable, hit)
		}
		if len(durable) >= limit {
			break
		}
	}
	if len(durable) > 0 {
		return durable, nil
	}
	return results, nil
}

func FormatMemoryHits(results []MemoryHit) string {
	if len(results) == 0 {
		return "No matching memories found."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d memory hits:\n", len(results))
	for _, hit := range results {
		ts := "-"
		if hit.CreatedAt > 0 {
			ts = time.Unix(hit.CreatedAt, 0).Format("01-02 15:04")
		}
		fmt.Fprintf(&b, "  [%s] %s %s\n    %s\n", ts, hit.Layer, hit.Source, hit.Text)
	}
	return b.String()
}

func memoryHitMatchesTopic(hit MemoryHit, topicID string) bool {
	if topicID == "" {
		return true
	}
	if strings.Contains(hit.Source, "topic="+topicID) || strings.Contains(hit.Source, "_"+topicID+"_") {
		return true
	}
	if strings.Contains(hit.Text, "topic="+topicID) || strings.Contains(hit.Text, "["+topicID+"]") || strings.Contains(hit.Text, "`"+topicID+"`") {
		return true
	}
	return false
}

func isDurableRecallLayer(layer string) bool {
	switch layer {
	case "P0", "P1", "L0", "L1":
		return true
	default:
		return false
	}
}

func adjustMemoryHitScore(query string, hit MemoryHit) float64 {
	score := hit.Score
	if isMetaMemoryText(hit.Text) && !isMetaMemoryQuery(query) {
		score -= 1.1
	}
	if containsDigit(normalizeMemoryText(query)) && !containsDigit(normalizeMemoryText(hit.Text)) {
		score -= 0.35
	}
	if score < 0 {
		return 0
	}
	return score
}

func isMetaMemoryText(text string) bool {
	text = normalizeMemoryText(text)
	return strings.Contains(text, "memory search") ||
		strings.Contains(text, "memory recent") ||
		strings.Contains(text, "memory compact") ||
		strings.Contains(text, "原样展示结果")
}

func isMetaMemoryQuery(query string) bool {
	query = normalizeMemoryText(query)
	return strings.Contains(query, "memory") || strings.Contains(query, "记忆") || strings.Contains(query, "搜索") || strings.Contains(query, "recent")
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func SearchMemorySemantic(db *sql.DB, queryEmbedding []float32, limit int) ([]Summary, error) {
	return nil, nil
}

func renderTrajectory(msgs []Message) string {
	var b strings.Builder
	for _, m := range msgs {
		switch m.Role {
		case "user":
			if m.Content != nil {
				fmt.Fprintf(&b, "[user] %s\n", *m.Content)
			}
		case "assistant":
			if len(m.ToolCalls) > 0 {
				for _, tc := range m.ToolCalls {
					fmt.Fprintf(&b, "[tool_call] %s(%s)\n", tc.Function.Name, tc.Function.Arguments)
				}
			}
			if m.Content != nil && *m.Content != "" {
				fmt.Fprintf(&b, "[assistant] %s\n", *m.Content)
			}
		case "tool":
			if m.Content != nil {
				fmt.Fprintf(&b, "[tool_result] %s\n", *m.Content)
			}
		}
	}
	return b.String()
}

func GenerateSummary(db *sql.DB, cfg *Config, newMsgs []Message) (string, error) {
	trajectory := renderTrajectory(newMsgs)
	if r := []rune(trajectory); len(r) > 6000 {
		trajectory = string(r[:6000]) + "\n... (truncated)"
	}

	var contextSection string
	recentSummaries, _ := GetRecentSummaries(db, 5)
	if len(recentSummaries) > 0 {
		contextSection = "近期对话摘要（作为上下文）:\n"
		for _, s := range recentSummaries {
			contextSection += "- " + s + "\n"
		}
		contextSection += "\n"
	}

	prompt := fmt.Sprintf(`%s请用1-3句话总结以下对话。包含：用户的意图、执行了什么操作、最终结果。

对话轨迹:
%s`, contextSection, trajectory)

	messages := []Message{
		TextMessage("system", "你是一个对话摘要生成器。只输出摘要，不要其他内容。中文输出。"),
		TextMessage("user", prompt),
	}

	resp, err := CallLLM(cfg, messages, nil, nil, nil, nil)
	if err != nil {
		for _, m := range newMsgs {
			if m.Role == "user" && m.Content != nil {
				text := *m.Content
				text = truncate(text, 100)
				return text, nil
			}
		}
		return "", err
	}

	return strings.TrimSpace(resp.Content), nil
}

type Fact struct {
	ID        int    `json:"id"`
	Content   string `json:"content"`
	Category  string `json:"category"`
	CreatedAt int64  `json:"created_at"`
}

func StoreFact(db *sql.DB, content, category string) error {
	facts, err := readFactFile()
	if err != nil {
		return err
	}
	if category == "" {
		category = "general"
	}
	nextID := 1
	if len(facts) > 0 {
		nextID = facts[0].ID + 1
		for _, f := range facts {
			if f.ID >= nextID {
				nextID = f.ID + 1
			}
		}
	}
	facts = append(facts, Fact{ID: nextID, Content: content, Category: category, CreatedAt: time.Now().Unix()})
	if err := writeFactFile(facts); err != nil {
		return err
	}
	return SyncLocalMemoryStore(db, "", "", content, "")
}

func ListFacts(db *sql.DB) ([]Fact, error) {
	return readFactFile()
}

func DeleteFact(db *sql.DB, id int) error {
	facts, err := readFactFile()
	if err != nil {
		return err
	}
	filtered := make([]Fact, 0, len(facts))
	for _, f := range facts {
		if f.ID == id {
			continue
		}
		filtered = append(filtered, f)
	}
	if err := writeFactFile(filtered); err != nil {
		return err
	}
	return SyncLocalMemoryStore(db, "", "", "", "")
}

func ProcessMemory(db *sql.DB, cfg *Config, topicID, runID string, newMsgs []Message) {
	var userMessage string
	for _, m := range newMsgs {
		if m.Role == "user" && m.Content != nil {
			userMessage = *m.Content
			break
		}
	}

	summary, _ := GenerateSummary(db, cfg, newMsgs)
	_ = UpdateSessionNodeSummaryByRunID(db, runID, summary)
	if err := SyncLocalMemoryStore(db, topicID, runID, userMessage, summary); err != nil {
		return
	}
}

func extractTopicID(hit MemoryHit) string {
	if idx := strings.Index(hit.Text, "topic="); idx >= 0 {
		rest := hit.Text[idx+len("topic="):]
		return firstToken(rest)
	}
	if idx := strings.Index(hit.Source, "topic="); idx >= 0 {
		rest := hit.Source[idx+len("topic="):]
		return firstToken(rest)
	}
	parts := strings.Split(filepathToSlash(hit.Source), "/")
	for i, part := range parts {
		if part == "runs" && i+2 < len(parts) {
			name := parts[len(parts)-1]
			segments := strings.Split(strings.TrimSuffix(name, ".md"), "_")
			if len(segments) >= 3 {
				return segments[1]
			}
		}
	}
	return ""
}

func extractRunID(hit MemoryHit) string {
	if idx := strings.Index(hit.Text, "run="); idx >= 0 {
		rest := hit.Text[idx+len("run="):]
		return firstToken(rest)
	}
	parts := strings.Split(filepath.Base(hit.Source), "_")
	if len(parts) >= 3 {
		return strings.TrimSuffix(parts[2], ".md")
	}
	return ""
}

func firstToken(s string) string {
	for i, r := range s {
		if r == ' ' || r == ']' || r == '`' {
			return s[:i]
		}
	}
	return s
}

func filepathToSlash(path string) string {
	return strings.ReplaceAll(path, "\\", "/")
}
