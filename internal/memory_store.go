package internal

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	localMemoryRecentRuns = 24
	localMemoryRecentView = 8
	localMemoryHotRunDays = 7
)

type MemoryHit struct {
	Text      string
	Source    string
	Layer     string
	Score     float64
	CreatedAt int64
}

func memoryRoot() string {
	return filepath.Join(dataRoot(), "memory")
}

func memoryRunsRoot() string {
	return filepath.Join(memoryRoot(), "runs")
}

func memoryRunDayDir(ts time.Time) string {
	return filepath.Join(memoryRunsRoot(), ts.Format("2006-01-02"))
}

func ensureMemoryStore() error {
	for _, dir := range []string{
		memoryRoot(),
		filepath.Join(memoryRoot(), "insights"),
		filepath.Join(memoryRoot(), "lessons"),
		filepath.Join(memoryRoot(), "archive"),
		memoryRunsRoot(),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func SyncLocalMemoryStore(db *sql.DB, topicID, runID, userMessage, summary string) error {
	if err := ensureMemoryStore(); err != nil {
		return err
	}

	now := time.Now()
	if summary != "" || userMessage != "" {
		if err := writeLocalRunNote(topicID, runID, userMessage, summary, now); err != nil {
			return err
		}
	}
	if err := refreshSessionState(db); err != nil {
		return err
	}
	if err := refreshInsightsDigest(db, now); err != nil {
		return err
	}
	if err := refreshLessonsDigest(db); err != nil {
		return err
	}
	if err := refreshMemoryOverview(db); err != nil {
		return err
	}
	if err := refreshMemoryAbstracts(now); err != nil {
		return err
	}
	return nil
}

func CompactLocalMemoryStore(db *sql.DB, keepDays int) (string, error) {
	if keepDays <= 0 {
		keepDays = localMemoryHotRunDays
	}
	if err := ensureMemoryStore(); err != nil {
		return "", err
	}

	entries, err := os.ReadDir(memoryRunsRoot())
	if err != nil {
		return "", err
	}

	cutoff := time.Now().AddDate(0, 0, -(keepDays - 1)).Truncate(24 * time.Hour)
	archiveRunsRoot := filepath.Join(memoryRoot(), "archive", "runs")
	if err := os.MkdirAll(archiveRunsRoot, 0o755); err != nil {
		return "", err
	}

	movedFiles := 0
	movedDays := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		day, err := time.Parse("2006-01-02", entry.Name())
		if err != nil || !day.Before(cutoff) {
			continue
		}

		srcDir := filepath.Join(memoryRunsRoot(), entry.Name())
		dstDir := filepath.Join(archiveRunsRoot, entry.Name())
		count, err := moveRunDayToArchive(srcDir, dstDir)
		if err != nil {
			return "", err
		}
		if count > 0 {
			movedDays++
			movedFiles += count
		}
	}

	now := time.Now()
	if err := refreshSessionState(db); err != nil {
		return "", err
	}
	if err := refreshMemoryOverview(db); err != nil {
		return "", err
	}
	if err := refreshMemoryAbstracts(now); err != nil {
		return "", err
	}

	if movedFiles == 0 {
		return fmt.Sprintf("memory already compact; kept last %d day(s) hot", keepDays), nil
	}
	return fmt.Sprintf("archived %d run note(s) from %d day folder(s); kept last %d day(s) hot", movedFiles, movedDays, keepDays), nil
}

func moveRunDayToArchive(srcDir, dstDir string) (int, error) {
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return 0, err
	}

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return 0, err
	}

	moved := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		srcPath := filepath.Join(srcDir, entry.Name())
		dstPath := filepath.Join(dstDir, entry.Name())
		if err := os.Rename(srcPath, dstPath); err != nil {
			return moved, err
		}
		moved++
	}

	if moved > 0 {
		if err := os.Remove(srcDir); err != nil && !os.IsNotExist(err) {
			return moved, err
		}
	}
	return moved, nil
}

func writeLocalRunNote(topicID, runID, userMessage, summary string, now time.Time) error {
	dayDir := memoryRunDayDir(now)
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		return err
	}

	fileName := fmt.Sprintf("%s_%s_%s.md", now.Format("150405"), topicID, runID)
	path := filepath.Join(dayDir, fileName)

	var b strings.Builder
	b.WriteString("# Run Memory\n\n")
	fmt.Fprintf(&b, "- topic: `%s`\n", topicID)
	fmt.Fprintf(&b, "- run: `%s`\n", runID)
	fmt.Fprintf(&b, "- time: `%s`\n\n", now.Format(time.RFC3339))
	b.WriteString("## Summary\n")
	if summary == "" {
		b.WriteString("(empty)\n")
	} else {
		b.WriteString(summary)
		b.WriteString("\n")
	}
	if userMessage != "" {
		b.WriteString("\n## User Intent\n")
		b.WriteString(userMessage)
		b.WriteString("\n")
	}

	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func refreshSessionState(db *sql.DB) error {
	rows, err := db.Query(`SELECT topic_id, COALESCE(run_id, ''), summary, created_at FROM summaries ORDER BY created_at DESC LIMIT ?`, localMemoryRecentView)
	if err != nil {
		return err
	}
	defer rows.Close()

	var items []string
	for rows.Next() {
		var topicID, runID, summary string
		var createdAt int64
		if err := rows.Scan(&topicID, &runID, &summary, &createdAt); err != nil {
			return err
		}
		ts := time.Unix(createdAt, 0).Format("01-02 15:04")
		items = append(items, fmt.Sprintf("- [%s] topic=%s run=%s %s", ts, topicID, blankFallback(runID, "-"), summary))
	}
	if err := rows.Err(); err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString("# Session State\n\n")
	b.WriteString("Working buffer for the latest runs. Keep it short and hot.\n\n")
	if len(items) == 0 {
		b.WriteString("(empty)\n")
	} else {
		b.WriteString(strings.Join(items, "\n"))
		b.WriteString("\n")
	}

	return os.WriteFile(filepath.Join(memoryRoot(), "SESSION-STATE.md"), []byte(b.String()), 0o644)
}

func refreshInsightsDigest(db *sql.DB, now time.Time) error {
	monthKey := now.Format("2006-01")
	rows, err := db.Query(`SELECT summary, created_at FROM summaries WHERE created_at >= ? ORDER BY created_at DESC LIMIT 12`, now.AddDate(0, -1, 0).Unix())
	if err != nil {
		return err
	}
	defer rows.Close()

	var items []string
	for rows.Next() {
		var summary string
		var createdAt int64
		if err := rows.Scan(&summary, &createdAt); err != nil {
			return err
		}
		ts := time.Unix(createdAt, 0).Format("01-02")
		items = append(items, fmt.Sprintf("- [%s] %s", ts, summary))
	}
	if err := rows.Err(); err != nil {
		return err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Insights %s\n\n", monthKey)
	b.WriteString("Monthly condensed view of recent summaries.\n\n")
	if len(items) == 0 {
		b.WriteString("(empty)\n")
	} else {
		b.WriteString(strings.Join(items, "\n"))
		b.WriteString("\n")
	}

	path := filepath.Join(memoryRoot(), "insights", monthKey+".md")
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func refreshLessonsDigest(db *sql.DB) error {
	facts, err := ListFacts(db)
	if err != nil {
		return err
	}

	var lines []string
	for _, f := range facts {
		lines = append(lines, fmt.Sprintf(`{"id":%d,"category":%q,"content":%q,"created_at":%d}`,
			f.ID, f.Category, f.Content, f.CreatedAt))
	}
	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}
	return os.WriteFile(filepath.Join(memoryRoot(), "lessons", "operational-lessons.jsonl"), []byte(content), 0o644)
}

func refreshMemoryOverview(db *sql.DB) error {
	facts, err := ListFacts(db)
	if err != nil {
		return err
	}
	rows, err := db.Query(`SELECT topic_id, summary, created_at FROM summaries ORDER BY created_at DESC LIMIT 12`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var recent []string
	for rows.Next() {
		var topicID, summary string
		var createdAt int64
		if err := rows.Scan(&topicID, &summary, &createdAt); err != nil {
			return err
		}
		ts := time.Unix(createdAt, 0).Format("01-02 15:04")
		recent = append(recent, fmt.Sprintf("- [P1][%s][%s] %s", ts, topicID, summary))
	}
	if err := rows.Err(); err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString("# Long-term Memory\n\n")
	b.WriteString("OpenViking-style lightweight memory layout: P0 facts, P1 distilled summaries, P2 working buffer.\n\n")
	b.WriteString("## P0 Stable Facts\n")
	if len(facts) == 0 {
		b.WriteString("- (none)\n")
	} else {
		for _, f := range facts {
			fmt.Fprintf(&b, "- [%s] %s\n", f.Category, f.Content)
		}
	}
	b.WriteString("\n## P1 Distilled Context\n")
	if len(recent) == 0 {
		b.WriteString("- (none)\n")
	} else {
		b.WriteString(strings.Join(recent, "\n"))
		b.WriteString("\n")
	}
	b.WriteString("\n## P2 Working Buffer\n")
	b.WriteString("- Read `SESSION-STATE.md` for the hottest run summaries.\n")
	b.WriteString("- Read `insights/` for month-level condensed notes.\n")
	b.WriteString("- Read `runs/YYYY-MM-DD/*.md` only when detailed evidence is needed.\n")

	return os.WriteFile(filepath.Join(memoryRoot(), "MEMORY.md"), []byte(b.String()), 0o644)
}

func refreshMemoryAbstracts(now time.Time) error {
	rootAbstract := strings.Join([]string{
		"# Memory Abstract",
		"",
		"- L0 root index for local memory.",
		"- `MEMORY.md`: durable facts and distilled context.",
		"- `SESSION-STATE.md`: hot working buffer for recent runs.",
		"- `insights/`: month-level condensed notes.",
		"- `lessons/`: structured operational facts.",
		"- `runs/`: raw per-run memory notes.",
		fmt.Sprintf("- last_refresh: %s", now.Format(time.RFC3339)),
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(memoryRoot(), ".abstract"), []byte(rootAbstract), 0o644); err != nil {
		return err
	}

	insightsAbstract := strings.Join([]string{
		"# Insights Abstract",
		"",
		"- Monthly rollups live here.",
		fmt.Sprintf("- latest_month: %s", now.Format("2006-01")),
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(memoryRoot(), "insights", ".abstract"), []byte(insightsAbstract), 0o644); err != nil {
		return err
	}

	lessonsAbstract := strings.Join([]string{
		"# Lessons Abstract",
		"",
		"- Structured facts exported as JSONL.",
		"- Source file: `operational-lessons.jsonl`.",
	}, "\n") + "\n"
	return os.WriteFile(filepath.Join(memoryRoot(), "lessons", ".abstract"), []byte(lessonsAbstract), 0o644)
}

func SearchLocalMemory(query string, limit int) ([]MemoryHit, error) {
	if limit <= 0 {
		limit = 5
	}
	if err := ensureMemoryStore(); err != nil {
		return nil, err
	}

	now := time.Now()
	var candidates []MemoryHit
	for _, path := range localMemoryCandidateFiles(now) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		layer := detectMemoryLayer(path)
		for _, chunk := range splitMemoryChunks(string(data)) {
			score := scoreMemoryChunk(query, chunk)
			if score <= 0 {
				continue
			}
			if layer == "L0" {
				score += 0.1
			}
			if layer == "P2" {
				score += 0.15
			}
			candidates = append(candidates, MemoryHit{
				Text:      chunk,
				Source:    filepath.ToSlash(strings.TrimPrefix(path, dataRoot()+string(os.PathSeparator))),
				Layer:     layer,
				Score:     score,
				CreatedAt: info.ModTime().Unix(),
			})
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score == candidates[j].Score {
			return candidates[i].CreatedAt > candidates[j].CreatedAt
		}
		return candidates[i].Score > candidates[j].Score
	})

	seen := make(map[string]bool)
	var results []MemoryHit
	for _, hit := range candidates {
		if seen[hit.Text] {
			continue
		}
		seen[hit.Text] = true
		results = append(results, hit)
		if len(results) >= limit {
			break
		}
	}
	return results, nil
}

func localMemoryCandidateFiles(now time.Time) []string {
	paths := []string{
		filepath.Join(memoryRoot(), ".abstract"),
		filepath.Join(memoryRoot(), "MEMORY.md"),
		filepath.Join(memoryRoot(), "SESSION-STATE.md"),
		filepath.Join(memoryRoot(), "insights", ".abstract"),
		filepath.Join(memoryRoot(), "insights", now.Format("2006-01")+".md"),
		filepath.Join(memoryRoot(), "lessons", ".abstract"),
		filepath.Join(memoryRoot(), "lessons", "operational-lessons.jsonl"),
	}

	for i := 0; i < localMemoryRecentRuns; i++ {
		day := now.AddDate(0, 0, -i)
		entries, err := os.ReadDir(memoryRunDayDir(day))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			paths = append(paths, filepath.Join(memoryRunDayDir(day), entry.Name()))
		}
	}
	return paths
}

func detectMemoryLayer(path string) string {
	switch {
	case strings.HasSuffix(path, ".abstract"):
		return "L0"
	case strings.Contains(path, string(filepath.Separator)+"insights"+string(filepath.Separator)):
		return "L1"
	case strings.Contains(path, string(filepath.Separator)+"lessons"+string(filepath.Separator)):
		return "P0"
	case strings.HasSuffix(path, "SESSION-STATE.md"):
		return "P2"
	case strings.HasSuffix(path, "MEMORY.md"):
		return "P1"
	default:
		return "L2"
	}
}

func splitMemoryChunks(content string) []string {
	lines := strings.Split(content, "\n")
	var chunks []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "- ")
		line = strings.TrimPrefix(line, "* ")
		chunks = append(chunks, line)
	}
	if len(chunks) == 0 && strings.TrimSpace(content) != "" {
		chunks = append(chunks, strings.TrimSpace(content))
	}
	return chunks
}

func scoreMemoryChunk(query, chunk string) float64 {
	q := normalizeMemoryText(query)
	c := normalizeMemoryText(chunk)
	if q == "" || c == "" {
		return 0
	}

	score := 0.0
	if strings.Contains(c, q) {
		score += 1.5
	}

	terms := expandMemoryTerms(query)
	matchedTerms := 0
	hasDigitTerm := false
	matchedDigitTerm := false
	for _, term := range terms {
		if term == "" {
			continue
		}
		if containsDigit(term) {
			hasDigitTerm = true
		}
		if strings.Contains(c, term) {
			score += 0.45
			matchedTerms++
			if containsDigit(term) {
				matchedDigitTerm = true
			}
		}
	}

	if len(terms) >= 2 {
		coverage := float64(matchedTerms) / float64(len(terms))
		score += coverage * 0.6
		if matchedTerms == 1 {
			score -= 0.25
		}
	}
	if hasDigitTerm && !matchedDigitTerm {
		score -= 0.6
	}
	if score < 0 {
		return 0
	}
	return score
}

func expandMemoryTerms(query string) []string {
	normalized := normalizeMemoryText(query)
	terms := make([]string, 0, 16)
	seen := make(map[string]bool)
	add := func(term string) {
		term = normalizeMemoryText(term)
		if len([]rune(term)) < 2 || seen[term] {
			return
		}
		seen[term] = true
		terms = append(terms, term)
	}

	for _, part := range strings.FieldsFunc(normalized, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r))
	}) {
		add(part)
	}

	runes := []rune(normalized)
	for i := 0; i < len(runes)-1; i++ {
		if isHan(runes[i]) && isHan(runes[i+1]) {
			add(string(runes[i : i+2]))
		}
	}

	if strings.Contains(normalized, "我叫") || strings.Contains(normalized, "名字") {
		add("身份")
		add("姓名")
	}
	if strings.Contains(normalized, "工作") || strings.Contains(normalized, "职业") {
		add("职业")
		add("工程师")
	}
	if strings.Contains(normalized, "喜欢") || strings.Contains(normalized, "偏好") {
		add("偏好")
		add("喜好")
	}
	if strings.Contains(normalized, "最近") || strings.Contains(normalized, "刚刚") {
		add("recent")
		add("latest")
	}

	return terms
}

func normalizeMemoryText(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastSpace := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			if !lastSpace {
				b.WriteRune(' ')
				lastSpace = true
			}
		case unicode.IsLetter(r) || unicode.IsDigit(r) || isHan(r):
			b.WriteRune(r)
			lastSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}

func isHan(r rune) bool {
	return unicode.In(r, unicode.Han)
}

func containsDigit(s string) bool {
	for _, r := range s {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func blankFallback(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func ReadLocalMemoryFile(name string) (string, error) {
	if err := ensureMemoryStore(); err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(memoryRoot(), name))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
