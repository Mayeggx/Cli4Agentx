package internal

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// --- Summary storage ---

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
	var embBlob []byte
	if len(embedding) > 0 {
		embBlob = EncodeEmbedding(embedding)
	}
	_, err := db.Exec(`INSERT INTO summaries (topic_id, run_id, summary, user_message, embedding, embedding_model, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		topicID, runID, summary, userMessage, embBlob, embeddingModel, time.Now().Unix())
	return err
}

func GetRecentSummaries(db *sql.DB, limit int) ([]string, error) {
	rows, err := db.Query(`SELECT summary FROM summaries ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var summaries []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		summaries = append(summaries, s)
	}
	for i, j := 0, len(summaries)-1; i < j; i, j = i+1, j-1 {
		summaries[i], summaries[j] = summaries[j], summaries[i]
	}
	return summaries, rows.Err()
}

// --- Search with filters ---

type SearchFilter struct {
	TopicID string // filter by topic
	Keyword string // keyword filter (applied after semantic/FTS)
	Limit   int
}

// SearchMemory combines semantic + keyword + lightweight lexical search.
func SearchMemory(db *sql.DB, cfg *Config, query string, filter SearchFilter) ([]Summary, error) {
	if filter.Limit == 0 {
		filter.Limit = 5
	}

	seen := make(map[int]bool)
	var results []Summary
	add := func(items []Summary) {
		for _, item := range items {
			if seen[item.ID] {
				continue
			}
			seen[item.ID] = true
			results = append(results, item)
		}
	}

	queryEmb, err := GetEmbedding(cfg, query)
	if err == nil && len(queryEmb) > 0 {
		if sem, err := searchSemantic(db, queryEmb, filter, 10); err == nil {
			add(sem)
		}
	}

	for _, variant := range searchKeywordVariants(query) {
		if kw, err := searchKeyword(db, variant, filter, 10); err == nil {
			add(kw)
		}
		if len(results) >= filter.Limit*2 {
			break
		}
	}

	if len(results) < filter.Limit {
		if lexical, err := searchLexical(db, query, filter, 10); err == nil {
			add(lexical)
		}
	}

	if filter.Keyword != "" {
		kw := strings.ToLower(filter.Keyword)
		var filtered []Summary
		for _, r := range results {
			if strings.Contains(strings.ToLower(r.SummaryText), kw) ||
				strings.Contains(strings.ToLower(r.UserMessage), kw) {
				filtered = append(filtered, r)
			}
		}
		results = filtered
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Similarity == results[j].Similarity {
			return results[i].CreatedAt > results[j].CreatedAt
		}
		return results[i].Similarity > results[j].Similarity
	})

	if len(results) > filter.Limit {
		results = results[:filter.Limit]
	}

	enrichTopicNames(db, results)
	return results, nil
}

func searchSemantic(db *sql.DB, queryEmbedding []float32, filter SearchFilter, limit int) ([]Summary, error) {
	query := `SELECT s.id, s.topic_id, COALESCE(s.run_id,''), s.summary, s.user_message, s.embedding, s.created_at
		FROM summaries s WHERE s.embedding IS NOT NULL`
	var args []any
	if filter.TopicID != "" {
		query += ` AND s.topic_id = ?`
		args = append(args, filter.TopicID)
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type scored struct {
		Summary
		sim float32
	}
	var results []scored

	for rows.Next() {
		var s Summary
		var embBlob []byte
		if err := rows.Scan(&s.ID, &s.TopicID, &s.RunID, &s.SummaryText, &s.UserMessage, &embBlob, &s.CreatedAt); err != nil {
			return nil, err
		}
		if len(embBlob) == 0 {
			continue
		}
		sim := CosineSimilarity(queryEmbedding, DecodeEmbedding(embBlob))
		if sim >= 0.45 {
			results = append(results, scored{s, sim})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].sim > results[j].sim
	})
	if len(results) > limit {
		results = results[:limit]
	}

	out := make([]Summary, len(results))
	for i, r := range results {
		r.Summary.Similarity = r.sim
		out[i] = r.Summary
	}
	return out, rows.Err()
}

func searchKeyword(db *sql.DB, query string, filter SearchFilter, limit int) ([]Summary, error) {
	sqlQuery := `SELECT s.id, s.topic_id, COALESCE(s.run_id,''), s.summary, s.user_message, s.created_at
		FROM summaries_fts fts
		JOIN summaries s ON s.id = fts.rowid
		WHERE summaries_fts MATCH ?`
	args := []any{query}
	if filter.TopicID != "" {
		sqlQuery += ` AND s.topic_id = ?`
		args = append(args, filter.TopicID)
	}
	sqlQuery += ` ORDER BY rank LIMIT ?`
	args = append(args, limit)

	rows, err := db.Query(sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Summary
	for rows.Next() {
		var s Summary
		if err := rows.Scan(&s.ID, &s.TopicID, &s.RunID, &s.SummaryText, &s.UserMessage, &s.CreatedAt); err != nil {
			return nil, err
		}
		results = append(results, s)
	}
	return results, rows.Err()
}

func searchKeywordVariants(query string) []string {
	variants := []string{query}
	for _, term := range expandMemoryTerms(query) {
		if term == "" {
			continue
		}
		variants = append(variants, term)
		if len(variants) >= 6 {
			break
		}
	}

	seen := make(map[string]bool)
	var out []string
	for _, variant := range variants {
		variant = strings.TrimSpace(variant)
		if variant == "" || seen[variant] {
			continue
		}
		seen[variant] = true
		out = append(out, variant)
	}
	return out
}

func searchLexical(db *sql.DB, query string, filter SearchFilter, limit int) ([]Summary, error) {
	sqlQuery := `SELECT id, topic_id, COALESCE(run_id,''), summary, user_message, created_at FROM summaries`
	var args []any
	if filter.TopicID != "" {
		sqlQuery += ` WHERE topic_id = ?`
		args = append(args, filter.TopicID)
	}

	rows, err := db.Query(sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type scored struct {
		Summary
		score float64
	}
	var items []scored
	for rows.Next() {
		var s Summary
		if err := rows.Scan(&s.ID, &s.TopicID, &s.RunID, &s.SummaryText, &s.UserMessage, &s.CreatedAt); err != nil {
			return nil, err
		}
		score := scoreMemoryChunk(query, s.SummaryText+" "+s.UserMessage)
		if score <= 0 {
			continue
		}
		s.Similarity = float32(minFloat(score/2.0, 0.99))
		items = append(items, scored{Summary: s, score: score})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].score == items[j].score {
			return items[i].CreatedAt > items[j].CreatedAt
		}
		return items[i].score > items[j].score
	})
	if len(items) > limit {
		items = items[:limit]
	}

	out := make([]Summary, len(items))
	for i, item := range items {
		out[i] = item.Summary
	}
	return out, nil
}

func enrichTopicNames(db *sql.DB, summaries []Summary) {
	cache := make(map[string]string)
	for i := range summaries {
		tid := summaries[i].TopicID
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

// FormatSearchResults renders DB search results with topic + run info.
func FormatSearchResults(results []Summary) string {
	if len(results) == 0 {
		return "No matching memories found."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d DB results:\n", len(results))
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

	var results []MemoryHit
	if localHits, err := SearchLocalMemory(query, limit*3); err == nil {
		for _, hit := range localHits {
			if filter.TopicID != "" && !memoryHitMatchesTopic(hit, filter.TopicID) {
				continue
			}
			if filter.TopicID != "" {
				hit.Score += 0.5
			}
			hit.Score = adjustMemoryHitScore(query, hit)
			if hit.Score <= 0 {
				continue
			}
			results = append(results, hit)
		}
	}
	if dbHits, err := SearchMemory(db, cfg, query, SearchFilter{TopicID: filter.TopicID, Keyword: filter.Keyword, Limit: limit * 2}); err == nil {
		for _, hit := range dbHits {
			layer := "DB"
			if hit.Similarity == 0 {
				layer = "DB-lexical"
			}
			score := float64(hit.Similarity)
			if filter.TopicID != "" {
				score += 0.35
			}
			memoryHit := MemoryHit{
				Text:      hit.SummaryText,
				Source:    fmt.Sprintf("db:topic=%s run=%s", hit.TopicID, blankFallback(hit.RunID, "-")),
				Layer:     layer,
				Score:     score,
				CreatedAt: hit.CreatedAt,
			}
			memoryHit.Score = adjustMemoryHitScore(query, memoryHit)
			if memoryHit.Score <= 0 {
				continue
			}
			results = append(results, memoryHit)
		}
	}

	if filter.Keyword != "" {
		kw := strings.ToLower(filter.Keyword)
		var filtered []MemoryHit
		for _, hit := range results {
			if strings.Contains(strings.ToLower(hit.Text), kw) {
				filtered = append(filtered, hit)
			}
		}
		results = filtered
	}
	if !isMetaMemoryQuery(query) {
		var filtered []MemoryHit
		for _, hit := range results {
			if isMetaMemoryText(hit.Text) {
				continue
			}
			filtered = append(filtered, hit)
		}
		if len(filtered) > 0 {
			results = filtered
		}
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].CreatedAt > results[j].CreatedAt
		}
		return results[i].Score > results[j].Score
	})

	seen := make(map[string]bool)
	var deduped []MemoryHit
	for _, hit := range results {
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

	seen := make(map[string]bool)
	var results []MemoryHit
	add := func(items []MemoryHit) {
		for _, item := range items {
			key := item.Layer + "|" + item.Text
			if seen[key] {
				continue
			}
			seen[key] = true
			results = append(results, item)
			if len(results) >= limit {
				return
			}
		}
	}

	if topicID != "" {
		if scoped, err := SearchAllMemory(db, cfg, query, SearchFilter{TopicID: topicID, Limit: limit}); err == nil {
			add(scoped)
		}
	}
	if len(results) < limit {
		if localHits, err := SearchLocalMemory(query, limit*4); err == nil {
			var durable []MemoryHit
			for _, hit := range localHits {
				if !isDurableRecallLayer(hit.Layer) {
					continue
				}
				durable = append(durable, hit)
			}
			add(durable)
		}
	}
	if len(results) > limit {
		results = results[:limit]
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

// --- Legacy compatibility ---

func SearchMemorySemantic(db *sql.DB, queryEmbedding []float32, limit int) ([]Summary, error) {
	return searchSemantic(db, queryEmbedding, SearchFilter{}, limit)
}

// --- Summary generation ---

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

// --- Facts ---

type Fact struct {
	ID        int    `json:"id"`
	Content   string `json:"content"`
	Category  string `json:"category"`
	CreatedAt int64  `json:"created_at"`
}

func StoreFact(db *sql.DB, content, category string) error {
	if category == "" {
		category = "general"
	}
	_, err := db.Exec(`INSERT INTO facts (content, category, created_at) VALUES (?, ?, ?)`,
		content, category, time.Now().Unix())
	if err != nil {
		return err
	}
	return SyncLocalMemoryStore(db, "", "", content, "")
}

func ListFacts(db *sql.DB) ([]Fact, error) {
	rows, err := db.Query(`SELECT id, content, category, created_at FROM facts ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facts []Fact
	for rows.Next() {
		var f Fact
		if err := rows.Scan(&f.ID, &f.Content, &f.Category, &f.CreatedAt); err != nil {
			return nil, err
		}
		facts = append(facts, f)
	}
	return facts, rows.Err()
}

func DeleteFact(db *sql.DB, id int) error {
	_, err := db.Exec(`DELETE FROM facts WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return SyncLocalMemoryStore(db, "", "", "", "")
}

// ProcessMemory generates summary and embedding for a completed Run.
func ProcessMemory(db *sql.DB, cfg *Config, topicID, runID string, newMsgs []Message) {
	var userMessage string
	for _, m := range newMsgs {
		if m.Role == "user" && m.Content != nil {
			userMessage = *m.Content
			break
		}
	}

	summary, _ := GenerateSummary(db, cfg, newMsgs)
	if summary == "" {
		_ = SyncLocalMemoryStore(db, topicID, runID, userMessage, "")
		return
	}

	embedding, _ := GetEmbedding(cfg, summary)
	if err := StoreSummary(db, topicID, runID, summary, userMessage, embedding, cfg.EmbeddingModel); err != nil {
		return
	}
	_ = SyncLocalMemoryStore(db, topicID, runID, userMessage, summary)
}
