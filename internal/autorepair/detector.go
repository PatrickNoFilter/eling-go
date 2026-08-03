package autorepair

import (
	"strings"
	"time"
)

// classifyClass maps an error string to a failure Class. It is intentionally
// conservative: only strong, non-input-dependent signals are raised to a
// specific broken class. Everything ambiguous falls back to transient (so a
// single failure is never escalated).
func classifyClass(err string, panicked bool, elapsed time.Duration) Class {
	if panicked {
		return ClassCrash
	}

	low := strings.ToLower(err)

	// Timeouts / rate-limits / connection blips are transient.
	if strings.Contains(low, "timed out") ||
		strings.Contains(low, "timeout") ||
		strings.Contains(low, "rate limit") ||
		strings.Contains(low, "too many requests") ||
		strings.Contains(low, "connection reset") ||
		strings.Contains(low, "aborted") {
		return ClassTransient
	}

	// Missing binaries / commands / package managers.
	if hasAny(low,
		"command not found",
		"exec: \"",           // exec: "ocr": executable file not found
		"executable file not found",
		"no such file or directory",
		"not installed",
		"npm err!",
		"apt-get",
		"could not find package",
		") not found",
	) {
		return ClassMissingDep
	}

	// Configuration / env / provider drift.
	if hasAny(low,
		"config",
		"base_url",
		"api key",
		"401",
		"403",
		"unauthorized",
		"invalid key",
		"mcp.enabled",
		"disabled",
		"provider",
	) {
		return ClassConfigDrift
	}

	// Contract / schema violations (malformed output).
	if hasAny(low,
		"invalid json",
		"json: cannot unmarshal",
		"unexpected end of json",
		"schema",
		"malformed",
		"cannot unmarshal",
	) {
		return ClassContractViolation
	}

	// Deterministic logic/render bugs.
	if hasAny(low,
		"logic",
		"render",
		"index out of range",
		"nil pointer",
		"panic:",
	) {
		return ClassLogicBug
	}

	return ClassUnknown
}

func hasAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// decideVerdict turns a Class + repeated-count into the Phase-0 verdict.
//
// The core question: "is this tool broken, and does it need autofix?"
//   - broken REQUIRES (a) a specific/crash class AND (b) the repeated threshold
//     (same error string), so one input-driven failure never escalates.
//   - transient is always retry (never mutated).
//   - autofix / quarantine are surfaced as verdicts but only acted on in
//     phases 1 & 2 (this phase only records them for the dashboard).
func decideVerdict(class Class, repeated int, threshold int) (Verdict, string) {
	switch class {
	case ClassTransient:
		return VerdictRetry, "transient; retry with backoff, no mutation"
	case ClassCrash:
		// A single crash is still quarantined (it can take the agent down).
		return VerdictQuarantine, "crash detected; recommend quarantine"
	case ClassContractViolation:
		if repeated >= threshold {
			return VerdictAdvisory, "schema/contract violation; advise manual check"
		}
		return VerdictNotBroken, "possible input issue; observing"
	case ClassMissingDep:
		if repeated >= threshold {
			return VerdictAutoFix, "missing dependency; autofix ready (phase 1)"
		}
		return VerdictNotBroken, "possible missing dep; observing"
	case ClassConfigDrift:
		if repeated >= threshold {
			return VerdictAutoFix, "config drift; autofix ready (phase 1)"
		}
		return VerdictNotBroken, "possible config issue; observing"
	case ClassLogicBug:
		if repeated >= threshold {
			return VerdictAdvisory, "deterministic logic bug; needs code change"
		}
		return VerdictNotBroken, "possible logic bug; observing"
	default: // ClassUnknown
		if repeated >= threshold {
			return VerdictAdvisory, "repeated unknown failure; manual review advised"
		}
		return VerdictNotBroken, "single failure; not yet considered broken"
	}
}

// ClassifyFailure computes the Class, Verdict and Reason for one failure given
// the engine's repeated count for that tool.
func (e *Engine) ClassifyFailure(f Failure) ClassifiedFailure {
	cf := ClassifiedFailure{
		Failure: f,
		Class:   classifyClass(f.Err, f.Panicked, f.Elapsed),
	}
	rep := e.Repeated(f.Tool)
	cf.Verdict, cf.Reason = decideVerdict(cf.Class, rep, e.thresholdN())
	return cf
}