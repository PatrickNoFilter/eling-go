// Package layers implements an 8-layer memory architecture for the ELING agent.
//
// Spec-kit verifier — check code implementation against spec/plan/tasks.
// Adapted from Python eling's spec_kit.py by PatrickNoFilter.
//
// Reads spec-kit artifacts (.specify/memory/constitution.md,
// specs/<feature>/spec.md, plan.md, tasks.md) and reports which
// requirements are covered by the current implementation.
package layers

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ── Artifact paths (spec-kit convention) ───────────────────────────────────

const (
	ConstitutionPath = ".specify/memory/constitution.md"
	SpecGlob         = "specs/*/spec.md"
	PlanGlob         = "specs/*/plan.md"
	TasksGlob        = "specs/*/tasks.md"
)

// ── Types ──────────────────────────────────────────────────────────────────

// SpecRequirement represents a single requirement extracted from a spec.md.
type SpecRequirement struct {
	Text       string   `json:"text"`
	Section    string   `json:"section"`
	SourceFile string   `json:"source_file"`
	Line       int      `json:"line"`
	Covered    bool     `json:"covered"`
	CoveredBy  []string `json:"covered_by"`
}

// SpecArtifact represents all spec-kit artifacts for a project.
type SpecArtifact struct {
	ProjectPath     string
	Constitution    string
	Requirements    []*SpecRequirement
	PlanSections    []string
	Tasks           []map[string]interface{}
	FeatureDirs     []string
	loaded          bool
}

// Detected returns true if spec-kit artifacts were found.
func (sa *SpecArtifact) Detected() bool {
	return sa.loaded && len(sa.Requirements) > 0
}

// CoveredCount returns the number of covered requirements.
func (sa *SpecArtifact) CoveredCount() int {
	count := 0
	for _, r := range sa.Requirements {
		if r.Covered {
			count++
		}
	}
	return count
}

// UncoveredCount returns the number of uncovered requirements.
func (sa *SpecArtifact) UncoveredCount() int {
	count := 0
	for _, r := range sa.Requirements {
		if !r.Covered {
			count++
		}
	}
	return count
}

// TotalCount returns the total number of requirements.
func (sa *SpecArtifact) TotalCount() int {
	return len(sa.Requirements)
}

// ── SpecKitVerifier ────────────────────────────────────────────────────────

// SpecKitVerifier verifies code implementation against spec-kit artifacts.
type SpecKitVerifier struct {
	ProjectPath string
}

// NewSpecKitVerifier creates a new SpecKitVerifier.
func NewSpecKitVerifier(projectPath string) *SpecKitVerifier {
	return &SpecKitVerifier{ProjectPath: projectPath}
}

// Detect checks if the project has spec-kit artifacts.
func (v *SpecKitVerifier) Detect() bool {
	specsRoot := filepath.Join(v.ProjectPath, "specs")
	info, err := os.Stat(specsRoot)
	if err == nil && info.IsDir() {
		return true
	}
	conPath := filepath.Join(v.ProjectPath, ConstitutionPath)
	_, err = os.Stat(conPath)
	return err == nil
}

// Load loads all spec-kit artifacts from the project.
func (v *SpecKitVerifier) Load() *SpecArtifact {
	artifact := &SpecArtifact{
		ProjectPath: v.ProjectPath,
	}

	// Constitution
	conPath := filepath.Join(v.ProjectPath, ConstitutionPath)
	if data, err := os.ReadFile(conPath); err == nil {
		text := string(data)
		if len(text) > 2000 {
			text = text[:2000]
		}
		artifact.Constitution = text
	}

	// Feature specs
	specDirs := findSpecDirs(v.ProjectPath)
	for _, sdir := range specDirs {
		artifact.FeatureDirs = append(artifact.FeatureDirs, filepath.Base(sdir))

		// spec.md
		specFile := filepath.Join(sdir, "spec.md")
		if data, err := os.ReadFile(specFile); err == nil {
			reqs := extractRequirements(string(data), specFile)
			artifact.Requirements = append(artifact.Requirements, reqs...)
		}

		// plan.md
		planFile := filepath.Join(sdir, "plan.md")
		if data, err := os.ReadFile(planFile); err == nil {
			sections := extractPlanSections(string(data))
			artifact.PlanSections = append(artifact.PlanSections, sections...)
		}

		// tasks.md
		tasksFile := filepath.Join(sdir, "tasks.md")
		if data, err := os.ReadFile(tasksFile); err == nil {
			tasks := extractTasks(string(data), tasksFile)
			artifact.Tasks = append(artifact.Tasks, tasks...)
		}
	}

	artifact.loaded = true
	return artifact
}

// Verify runs spec-kit verification and returns a report.
// changedFiles: files that were modified in the current session.
// allFiles: all project files (for coverage analysis). Auto-discovered if nil.
func (v *SpecKitVerifier) Verify(changedFiles, allFiles []string) map[string]interface{} {
	if !v.Detect() {
		return map[string]interface{}{
			"detected": false,
			"summary":  "No spec-kit artifacts found (no specs/ directory)",
			"nudge":    "",
			"requirements": []map[string]interface{}{},
			"coverage": map[string]interface{}{
				"covered":   0,
				"uncovered": 0,
				"total":     0,
			},
		}
	}

	artifact := v.Load()

	if len(artifact.Requirements) == 0 {
		return map[string]interface{}{
			"detected": true,
			"summary":  "Spec-kit detected but no requirements extracted",
			"nudge":    "",
			"requirements": []map[string]interface{}{},
			"coverage": map[string]interface{}{
				"covered":   0,
				"uncovered": 0,
				"total":     0,
			},
		}
	}

	// Discover all project files if not provided
	if allFiles == nil {
		allFiles = discoverProjectFiles(v.ProjectPath)
	}

	if changedFiles == nil {
		changedFiles = []string{}
	}

	computeCoverage(artifact.Requirements, changedFiles, allFiles)

	nudge := buildSpecNudge(artifact, changedFiles)

	// Build requirements as maps
	reqMaps := make([]map[string]interface{}, 0, len(artifact.Requirements))
	for _, r := range artifact.Requirements {
		reqMaps = append(reqMaps, map[string]interface{}{
			"text":        truncateStr(r.Text, 120),
			"section":     r.Section,
			"source_file": r.SourceFile,
			"line":        r.Line,
			"covered":     r.Covered,
			"covered_by":  r.CoveredBy,
		})
	}

	return map[string]interface{}{
		"detected": true,
		"summary": itoa(artifact.CoveredCount()) + "/" + itoa(artifact.TotalCount()) +
			" requirements covered (" + itoa(artifact.UncoveredCount()) + " uncovered)",
		"nudge": nudge,
		"requirements": reqMaps,
		"coverage": map[string]interface{}{
			"covered":   artifact.CoveredCount(),
			"uncovered": artifact.UncoveredCount(),
			"total":     artifact.TotalCount(),
		},
		"features":             artifact.FeatureDirs,
		"tasks":                len(artifact.Tasks),
		"constitution_present": artifact.Constitution != "",
	}
}

// ── Internal helpers ───────────────────────────────────────────────────────

func findSpecDirs(projectPath string) []string {
	specsRoot := filepath.Join(projectPath, "specs")
	info, err := os.Stat(specsRoot)
	if err != nil || !info.IsDir() {
		return nil
	}

	entries, err := os.ReadDir(specsRoot)
	if err != nil {
		return nil
	}

	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(specsRoot, e.Name()))
		}
	}
	sort.Strings(dirs)
	return dirs
}

type mdSection struct {
	Heading string
	Body    string
	Line    int
}

func parseMarkdownSections(text string) []mdSection {
	var sections []mdSection
	currentHeading := "preamble"
	var currentBody []string
	startLine := 0
	lines := strings.Split(text, "\n")

	headingRe := regexp.MustCompile(`^#{1,4}\s+(.+)$`)

	for i, line := range lines {
		m := headingRe.FindStringSubmatch(line)
		if m != nil {
			if len(currentBody) > 0 {
				sections = append(sections, mdSection{
					Heading: currentHeading,
					Body:    strings.TrimSpace(strings.Join(currentBody, "\n")),
					Line:    startLine,
				})
			}
			currentHeading = strings.TrimSpace(m[1])
			currentBody = nil
			startLine = i + 1 // 1-indexed
		} else {
			currentBody = append(currentBody, line)
		}
	}

	if len(currentBody) > 0 {
		sections = append(sections, mdSection{
			Heading: currentHeading,
			Body:    strings.TrimSpace(strings.Join(currentBody, "\n")),
			Line:    startLine,
		})
	}

	return sections
}

func extractRequirements(text, sourceFile string) []*SpecRequirement {
	var reqs []*SpecRequirement
	currentSection := ""
	lines := strings.Split(text, "\n")

	headingRe := regexp.MustCompile(`^#{1,4}\s+(.+)$`)
	bulletRe := regexp.MustCompile(`^[-*+]\s+(.+)$`)
	numberedRe := regexp.MustCompile(`^\d+[.)]\s+(.+)$`)
	checklistRe := regexp.MustCompile(`^[-*+]\s+\[\s*[ xX]?\s*\]\s+(.+)$`)

	for i, line := range lines {
		// Track section headings
		if hm := headingRe.FindStringSubmatch(line); hm != nil {
			currentSection = strings.TrimSpace(hm[1])
			continue
		}

		stripped := strings.TrimSpace(line)

		// Bullet/list items
		if m := bulletRe.FindStringSubmatch(stripped); m != nil {
			content := strings.TrimSpace(m[1])
			if len(content) > 15 && !strings.HasPrefix(content, "[") {
				reqs = append(reqs, &SpecRequirement{
					Text:       content,
					Section:    currentSection,
					SourceFile: sourceFile,
					Line:       i + 1,
				})
			}
			continue
		}

		// Numbered items
		if m := numberedRe.FindStringSubmatch(stripped); m != nil {
			content := strings.TrimSpace(m[1])
			if len(content) > 15 {
				reqs = append(reqs, &SpecRequirement{
					Text:       content,
					Section:    currentSection,
					SourceFile: sourceFile,
					Line:       i + 1,
				})
			}
			continue
		}

		// Checklist items
		if m := checklistRe.FindStringSubmatch(stripped); m != nil {
			content := strings.TrimSpace(m[1])
			if len(content) > 10 {
				reqs = append(reqs, &SpecRequirement{
					Text:       content,
					Section:    currentSection,
					SourceFile: sourceFile,
					Line:       i + 1,
				})
			}
			continue
		}
	}

	return reqs
}

func extractTasks(text, sourceFile string) []map[string]interface{} {
	var tasks []map[string]interface{}
	currentSection := ""
	lines := strings.Split(text, "\n")

	headingRe := regexp.MustCompile(`^#{1,4}\s+(.+)$`)
	checklistRe := regexp.MustCompile(`^[-*+]\s+\[\s*[ xX]?\s*\]\s+(.+)$`)
	fileRefRe := regexp.MustCompile("`([^`]+)`")

	for i, line := range lines {
		if hm := headingRe.FindStringSubmatch(line); hm != nil {
			currentSection = strings.TrimSpace(hm[1])
			continue
		}

		stripped := strings.TrimSpace(line)
		if m := checklistRe.FindStringSubmatch(stripped); m != nil {
			content := strings.TrimSpace(m[1])
			fileRefs := fileRefRe.FindAllStringSubmatch(content, -1)
			var refs []string
			for _, ref := range fileRefs {
				refs = append(refs, ref[1])
			}
			task := map[string]interface{}{
				"task":        truncateStr(content, 200),
				"file_refs":   refs,
				"section":     currentSection,
				"source_file": sourceFile,
				"line":        i + 1,
			}
			tasks = append(tasks, task)
		}
	}

	return tasks
}

func extractPlanSections(text string) []string {
	var sections []string
	relevantKeywords := []string{
		"implementation", "architecture", "component", "api",
		"data model", "database", "frontend", "backend",
	}

	for _, sec := range parseMarkdownSections(text) {
		headingLower := strings.ToLower(sec.Heading)
		for _, kw := range relevantKeywords {
			if strings.Contains(headingLower, kw) {
				body := sec.Body
				if len(body) > 500 {
					body = body[:500]
				}
				sections = append(sections, "## "+sec.Heading+"\n\n"+body)
				break
			}
		}
	}

	return sections
}

// ── Coverage analysis ──────────────────────────────────────────────────────

var stopWords = map[string]bool{
	"should": true, "would": true, "could": true, "must": true, "shall": true,
	"will": true, "need": true, "able": true, "used": true, "use": true,
	"using": true, "user": true, "users": true, "also": true, "well": true,
	"one": true, "two": true, "new": true, "make": true, "made": true,
	"support": true, "based": true, "within": true, "without": true,
	"across": true, "after": true, "before": true, "between": true,
	"other": true, "each": true, "every": true, "both": true, "first": true,
	"last": true, "being": true, "done": true, "does": true, "doing": true,
	"having": true, "have": true, "has": true, "than": true, "then": true,
	"that": true, "this": true, "these": true, "those": true, "which": true,
	"what": true, "when": true, "where": true, "their": true, "them": true,
	"they": true, "your": true, "from": true, "into": true, "over": true,
	"such": true, "some": true, "more": true, "most": true, "many": true,
	"much": true, "very": true, "just": true, "about": true, "down": true,
	"back": true, "still": true, "already": true, "always": true, "never": true,
	"ever": true, "here": true, "there": true, "only": true, "really": true,
	"way": true, "thing": true, "things": true,
}

func computeCoverage(requirements []*SpecRequirement, changedFiles, allProjectFiles []string) {
	for _, req := range requirements {
		// Extract significant terms from requirement
		terms := make(map[string]bool)
		for _, word := range strings.Fields(strings.ToLower(req.Text)) {
			word = strings.Trim(word, ".,;:!?()[]{}\"'")
			if len(word) > 3 && !stopWords[word] {
				terms[word] = true
			}
		}

		req.CoveredBy = nil
		coveredSet := make(map[string]bool)

		// Check all project files
		for _, fpath := range allProjectFiles {
			fpLower := strings.ToLower(fpath)
			for term := range terms {
				if strings.Contains(fpLower, term) {
					coveredSet[fpath] = true
					break
				}
			}
		}

		// Check changed files specifically
		for _, cf := range changedFiles {
			for term := range terms {
				if strings.Contains(strings.ToLower(cf), term) {
					coveredSet[cf] = true
					break
				}
			}
		}

		for p := range coveredSet {
			req.CoveredBy = append(req.CoveredBy, p)
		}
		sort.Strings(req.CoveredBy)

		req.Covered = len(req.CoveredBy) > 0
	}
}

// ── Nudge builder ──────────────────────────────────────────────────────────

func buildSpecNudge(artifact *SpecArtifact, changedFiles []string) string {
	if !artifact.Detected() {
		return ""
	}

	var uncovered []*SpecRequirement
	for _, r := range artifact.Requirements {
		if !r.Covered {
			uncovered = append(uncovered, r)
		}
	}

	if len(uncovered) == 0 {
		return ""
	}

	var lines []string
	lines = append(lines, "[System: Spec-kit requirements pending verification", "")

	if artifact.Constitution != "" {
		constitution := artifact.Constitution
		if len(constitution) > 80 {
			constitution = constitution[:80]
		}
		lines = append(lines, "Constitution: "+constitution+"...")
	}

	lines = append(lines, "\nSpec coverage: "+itoa(artifact.CoveredCount())+"/"+itoa(artifact.TotalCount())+
		" requirements covered by code")

	if len(artifact.Tasks) > 0 {
		lines = append(lines, "Tasks defined: "+itoa(len(artifact.Tasks)))
	}

	if len(uncovered) > 0 {
		lines = append(lines, "\nUncovered requirements ("+itoa(len(uncovered))+ "):")
		shown := uncovered
		if len(shown) > 5 {
			shown = shown[:5]
		}
		for _, req := range shown {
			sectionTag := ""
			if req.Section != "" {
				sectionTag = "[" + req.Section + "] "
			}
			text := req.Text
			if len(text) > 100 {
				text = text[:100]
			}
			lines = append(lines, "  "+sectionTag+text)
		}
		if len(uncovered) > 5 {
			lines = append(lines, "  ... and "+itoa(len(uncovered)-5)+" more")
		}
	}

	if len(changedFiles) > 0 {
		lines = append(lines, "\nRecently changed files ("+itoa(len(changedFiles))+ "):")
		shown := changedFiles
		if len(shown) > 5 {
			shown = shown[:5]
		}
		for _, cf := range shown {
			lines = append(lines, "  - "+cf)
		}
		if len(changedFiles) > 5 {
			lines = append(lines, "  ... and "+itoa(len(changedFiles)-5)+" more")
		}
	}

	lines = append(lines,
		"\nReview the spec requirements above and ensure the implementation"+
			"\naddresses each one. Run verification (tests/lint/build) and"+
			"\nrecord the result with eling_verify.]")

	return strings.Join(lines, "\n")
}

// ── Utilities ──────────────────────────────────────────────────────────────

func discoverProjectFiles(projectPath string) []string {
	var files []string
	filepath.Walk(projectPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		// Skip hidden dirs and common non-project dirs
		rel, err := filepath.Rel(projectPath, path)
		if err != nil {
			return nil
		}
		parts := strings.Split(rel, string(filepath.Separator))
		for _, part := range parts {
			if part == ".git" || part == "__pycache__" || part == "node_modules" || part == ".specify" {
				return filepath.SkipDir
			}
		}
		if !info.IsDir() {
			files = append(files, rel)
		}
		return nil
	})
	return files
}