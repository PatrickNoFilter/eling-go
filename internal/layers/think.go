// Package layers implements an 8-layer memory architecture for the ELING agent.
//
// Think method — synthesis + gap-analysis for the Brain.
// Adapted from Python eling's brain.py:think() by PatrickNoFilter.
//
// think is the expensive path — kept behind an explicit tool call so the
// cheap recall path stays unchanged. It returns:
//   - query, synthesis (summary)
//   - results (merged recall)
//   - reason_results (compositional entity reasoning)
//   - gap_analysis { stale_count, stale_facts, contradicted_count,
//                    contradicted_facts, unknown_count }
package layers

import (
	"context"
	"strconv"
	"strings"
)

// ThinkResult is the result of a think operation.
type ThinkResult struct {
	Query         string      `json:"query"`
	Synthesis     string      `json:"synthesis"`
	Results       []Result    `json:"results"`
	ReasonResults []Result    `json:"reason_results"`
	GapAnalysis   GapAnalysis `json:"gap_analysis"`
}

// GapAnalysis reports stale, contradicted, and unknown facts.
type GapAnalysis struct {
	StaleCount        int       `json:"stale_count"`
	StaleFacts        []GapFact `json:"stale_facts"`
	ContradictedCount int       `json:"contradicted_count"`
	ContradictedFacts []GapFact `json:"contradicted_facts"`
	UnknownCount      int       `json:"unknown_count"`
}

// GapFact describes a fact found during gap analysis.
type GapFact struct {
	FactID   int64   `json:"fact_id,omitempty"`
	Content  string  `json:"content"`
	Strength float64 `json:"strength,omitempty"`
	Source   string  `json:"source,omitempty"`
	Tags     string  `json:"tags,omitempty"`
}

// ── Brain.Think ────────────────────────────────────────────────────────────

// Think performs synthesis + gap-analysis: recall + reason,
// then report stale/contradicted/unknown.
//
// This is the expensive path — kept behind an explicit tool call so the
// cheap recall path stays unchanged.
func (b *Brain) Think(ctx context.Context, query string, entities []string, limit int) *ThinkResult {
	// Empty-query short-circuit
	if query == "" {
		return &ThinkResult{
			Query:     query,
			Synthesis: "No query provided.",
			Results:   nil,
			GapAnalysis: GapAnalysis{
				UnknownCount: 1,
			},
		}
	}

	if limit <= 0 {
		limit = 10
	}

	// 1. Raw recall (cheap, unchanged)
	merged, _ := b.Query(ctx, query, limit)

	// 2. Reason if entities provided (compositional)
	var reasonResults []Result
	if len(entities) > 0 {
		reasonResults, _ = b.Reason(ctx, entities, "", limit)
	}

	// 3. Gap analysis — scan recall results for stale / contradicted
	staleFacts := findStaleFacts(merged, ActiveThreshold)
	contradictedFacts := findContradictedFacts(merged)

	seenIDs := make(map[int64]bool)
	for _, r := range merged {
		fid := parseFactID(r.Metadata["fact_id"])
		if fid > 0 {
			seenIDs[fid] = true
		}
	}

	// Also check reason results for stale/contradicted
	for _, r := range reasonResults {
		factID := parseFactID(r.Metadata["fact_id"])
		if factID > 0 {
			if seenIDs[factID] {
				continue
			}
			seenIDs[factID] = true
		}

		strength := parseFloat64(r.Metadata["strength"], 1.0)
		tags := r.Metadata["tags"]

		if strength < ActiveThreshold {
			staleFacts = append(staleFacts, GapFact{
				FactID:   factID,
				Content:  thinkContent(r),
				Strength: strength,
				Source:   r.Source,
			})
		}

		if strings.Contains(tags, "contradiction_pending") {
			contradictedFacts = append(contradictedFacts, GapFact{
				FactID:  factID,
				Content: thinkContent(r),
				Tags:    tags,
			})
		}
	}

	unknownCount := 0
	if len(merged) == 0 {
		unknownCount = 1
	}

	// Programmatic synthesis
	var parts []string
	nFacts := len(merged)

	if nFacts > 0 {
		parts = append(parts, "Found "+itoa(nFacts)+" result")
		if nFacts != 1 {
			parts[len(parts)-1] += "s"
		}
		parts[len(parts)-1] += "."
	} else {
		parts = append(parts, "No relevant facts found — this appears to be new/unexplored information.")
	}

	if len(staleFacts) > 0 {
		parts = append(parts, itoa(len(staleFacts))+" fact")
		if len(staleFacts) != 1 {
			parts[len(parts)-1] += "s"
		}
		parts[len(parts)-1] += " are stale (strength < " + float64ToString(ActiveThreshold) + ")."
	}

	if len(contradictedFacts) > 0 {
		parts = append(parts, itoa(len(contradictedFacts))+" fact")
		if len(contradictedFacts) != 1 {
			parts[len(parts)-1] += "s"
		}
		parts[len(parts)-1] += " are flagged as contradicted."
	}

	if len(entities) > 0 {
		parts = append(parts, "Reasoned across "+itoa(len(entities))+" entit")
		if len(entities) == 1 {
			parts[len(parts)-1] += "y"
		} else {
			parts[len(parts)-1] += "ies"
		}
		parts[len(parts)-1] += ": " + strings.Join(entities, ", ") + "."
	}

	return &ThinkResult{
		Query:     query,
		Synthesis: strings.Join(parts, " "),
		Results:   merged,
		ReasonResults: reasonResults,
		GapAnalysis: GapAnalysis{
			StaleCount:        len(staleFacts),
			StaleFacts:        truncateGapFacts(staleFacts, 5),
			ContradictedCount: len(contradictedFacts),
			ContradictedFacts: truncateGapFacts(contradictedFacts, 5),
			UnknownCount:      unknownCount,
		},
	}
}

// ── Internal helpers ───────────────────────────────────────────────────────

func findStaleFacts(results []Result, threshold float64) []GapFact {
	var stale []GapFact
	for _, r := range results {
		strength := parseFloat64(r.Metadata["strength"], 1.0)
		if strength < threshold {
			factID := parseFactID(r.Metadata["fact_id"])
			stale = append(stale, GapFact{
				FactID:   factID,
				Content:  thinkContent(r),
				Strength: strength,
				Source:   r.Source,
			})
		}
	}
	return stale
}

func findContradictedFacts(results []Result) []GapFact {
	var contradicted []GapFact
	for _, r := range results {
		tags := r.Metadata["tags"]
		if strings.Contains(tags, "contradiction_pending") {
			factID := parseFactID(r.Metadata["fact_id"])
			contradicted = append(contradicted, GapFact{
				FactID:  factID,
				Content: thinkContent(r),
				Tags:    tags,
			})
		}
	}
	return contradicted
}

func thinkContent(r Result) string {
	if r.Content != "" {
		if len(r.Content) > 200 {
			return r.Content[:200]
		}
		return r.Content
	}
	if r.Source != "" {
		return r.Source
	}
	return "unknown"
}

func truncateGapFacts(facts []GapFact, max int) []GapFact {
	if len(facts) <= max {
		return facts
	}
	return facts[:max]
}

// parseFactID parses a fact ID from a metadata string value.
func parseFactID(s string) int64 {
	if s == "" {
		return 0
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// parseFloat64 parses a float64 from a metadata string value.
func parseFloat64(s string, defaultVal float64) float64 {
	if s == "" {
		return defaultVal
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return defaultVal
	}
	return f
}

// float64ToString converts a float64 to a string with 2 decimal places.
func float64ToString(f float64) string {
	return strconv.FormatFloat(f, 'f', 2, 64)
}
