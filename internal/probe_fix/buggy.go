// Package probe_fix is a temporary package used to validate that ELING can
// perform a real agentic code edit using the local LLM provider (Phase 6 of
// local_llm_plan.md). It is removed after verification.
package probe_fix

// Reverse returns s with its characters reversed.
func Reverse(s string) string {
	b := []byte(s)
	n := len(b)
	for i := 0; i < n/2; i++ {
		b[i], b[n-1-i] = b[n-1-i], b[i]
	}
	return string(b)
}
