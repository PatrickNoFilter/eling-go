package benchmark

// ──────────────────────────────────────────────────────────────────────
// DefaultTestCases returns a comprehensive set of built-in test cases
// covering all capability dimensions of ELING.
// These serve as the standard benchmark suite.
// ──────────────────────────────────────────────────────────────────────

// DefaultTestCases returns the complete built-in benchmark suite.
func DefaultTestCases() []TestCase {
	var cases []TestCase

	// ── Memory Tests ─────────────────────────────────────────────────
	cases = append(cases, MemoryCases()...)

	// ── Session Tests ────────────────────────────────────────────────
	cases = append(cases, SessionCases()...)

	return cases
}

// ──────────────────────────────────────────────────────────────────────
// Memory Benchmark Cases
// ──────────────────────────────────────────────────────────────────────

// MemoryCases returns test cases for memory operations.
func MemoryCases() []TestCase {
	return []TestCase{
		{
			ID:              "mem-store-1",
			Description:     "Store a memory item and verify it exists",
			Suite:           SuiteMemory,
			Severity:        SeverityCritical,
			Input:           "Remember that the database URL is postgres://localhost:5432/mydb",
			ExpectedSuccess: true,
			Tags:            []string{"memory", "store"},
		},
		{
			ID:              "mem-store-2",
			Description:     "Store multiple memory items",
			Suite:           SuiteMemory,
			Severity:        SeverityHigh,
			Input:           "Remember: API key is env var, port is 8080, debug mode is off",
			ExpectedSuccess: true,
			Tags:            []string{"memory", "store"},
		},
		{
			ID:              "mem-recall-1",
			Description:     "Recall a stored fact",
			Suite:           SuiteMemory,
			Severity:        SeverityCritical,
			Input:           "What is the database URL?",
			ExpectedSuccess: true,
			ExpectedOutput:  "postgres://localhost:5432/mydb",
			Tags:            []string{"memory", "recall"},
		},
		{
			ID:              "mem-recall-2",
			Description:     "Recall with partial match",
			Suite:           SuiteMemory,
			Severity:        SeverityMedium,
			Input:           "database",
			ExpectedSuccess: true,
			ExpectedOutput:  "postgres",
			Tags:            []string{"memory", "recall"},
		},
		{
			ID:              "mem-category-1",
			Description:     "Memory items have correct categories",
			Suite:           SuiteMemory,
			Severity:        SeverityMedium,
			Input:           "category:fact",
			ExpectedSuccess: true,
			Tags:            []string{"memory", "category"},
		},
		{
			ID:              "mem-decay-1",
			Description:     "Memory decay removes weak items",
			Suite:           SuiteMemory,
			Severity:        SeverityHigh,
			Input:           "Apply memory decay and verify weak items are removed",
			ExpectedSuccess: true,
			Tags:            []string{"memory", "decay"},
		},
	}
}

// ──────────────────────────────────────────────────────────────────────
// Session Benchmark Cases
// ──────────────────────────────────────────────────────────────────────

// SessionCases returns test cases for session management.
func SessionCases() []TestCase {
	return []TestCase{
		{
			ID:              "ses-create-1",
			Description:     "Create a new session",
			Suite:           SuiteSession,
			Severity:        SeverityCritical,
			Input:           "Create a new session named 'benchmark-test'",
			ExpectedSuccess: true,
			Tags:            []string{"session", "create"},
		},
		{
			ID:              "ses-append-1",
			Description:     "Append messages to session",
			Suite:           SuiteSession,
			Severity:        SeverityHigh,
			Input:           "Add two messages to the session",
			ExpectedSuccess: true,
			ExpectedOutput:  "messages",
			Tags:            []string{"session", "append"},
		},
		{
			ID:              "ses-resume-1",
			Description:     "Resume an existing session",
			Suite:           SuiteSession,
			Severity:        SeverityCritical,
			Input:           "Resume the 'benchmark-test' session",
			ExpectedSuccess: true,
			Tags:            []string{"session", "resume"},
		},
		{
			ID:              "ses-list-1",
			Description:     "List all sessions",
			Suite:           SuiteSession,
			Severity:        SeverityMedium,
			Input:           "List all available sessions",
			ExpectedSuccess: true,
			Tags:            []string{"session", "list"},
		},
		{
			ID:              "ses-metadata-1",
			Description:     "Session metadata is preserved",
			Suite:           SuiteSession,
			Severity:        SeverityMedium,
			Input:           "Check that session metadata contains expected keys",
			ExpectedSuccess: true,
			Tags:            []string{"session", "metadata"},
		},
	}
}
