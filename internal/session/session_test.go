package session

import (
	"fmt"
	"sync"
	"testing"
)

// TestConcurrentAppendAndGetCopy is a regression test for the data race where
// TUI /stats, GetStats and GetSession read the live session Entries slice
// while an Ask goroutine (or its interrupted-save defer) appends to it.
//
// Before the fix, Get() returned the live *Session and readers iterated
// s.Entries AFTER the manager lock was released — a data race that could
// corrupt the slice header and eventually trigger the production crash
// "reflect: slice index out of range" in SaveState's json.MarshalIndent.
//
// With GetCopy / SetLastEntryTokens / SetMetadata / LastEntry, every access
// happens under the manager lock, so this test must complete with zero
// panics and a monotonically growing entry count.
func TestConcurrentAppendAndGetCopy(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Create("test", "model-x")

	const writers = 4
	const readsPerWriter = 200

	var wg sync.WaitGroup

	// Writers: append user/assistant entries + mutate tokens + metadata.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < readsPerWriter; i++ {
				if err := m.Append("test", "user", fmt.Sprintf("u-%d-%d", id, i)); err != nil {
					t.Errorf("append user: %v", err)
					return
				}
				if err := m.AppendWithReasoning("test", "assistant", fmt.Sprintf("a-%d-%d", id, i), "think"); err != nil {
					t.Errorf("append assistant: %v", err)
					return
				}
				m.SetLastEntryTokens("test", i*10)
				_ = m.SetMetadata("test", "total_tokens", fmt.Sprintf("%d", i))
				_ = m.SetMetadata("test", fmt.Sprintf("k-%d", id), "v")
			}
		}(w)
	}

	// Readers: take snapshots via GetCopy and read every entry + metadata.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < readsPerWriter; i++ {
				s, ok := m.GetCopy("test")
				if !ok || s == nil {
					t.Error("GetCopy returned no session")
					return
				}
				// Walk the full copied slice and metadata map — would panic
				// on a corrupted slice header under the old live-pointer race.
				for _, e := range s.Entries {
					_ = e.Role
					_ = e.Content
				}
				for k, v := range s.Metadata {
					_ = k
					_ = v
				}
				if _, ok := m.LastEntry("test"); !ok {
					// Allowed only while writers are mid-first-append; the
					// writers append synchronously so this is rare. Don't fail.
					continue
				}
			}
		}()
	}

	wg.Wait()

	// After all writers finish, the session must contain all entries.
	final, ok := m.GetCopy("test")
	if !ok {
		t.Fatal("session missing after writers")
	}
	expected := writers * readsPerWriter * 2
	if len(final.Entries) != expected {
		t.Fatalf("expected %d entries, got %d", expected, len(final.Entries))
	}
	if _, ok := final.Metadata["total_tokens"]; !ok {
		t.Error("expected total_tokens metadata to be set")
	}
}

// TestGetReturnsLivePointerButCopyIsDetached verifies that mutating a GetCopy
// result does not affect the stored session (deep-copy semantics), while
// Append still updates the live session.
func TestGetCopyIsDetached(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Create("test", "model-x")

	_ = m.Append("test", "user", "hello")
	_ = m.Append("test", "assistant", "world")

	copy1, ok := m.GetCopy("test")
	if !ok {
		t.Fatal("GetCopy failed")
	}
	if len(copy1.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(copy1.Entries))
	}

	// Mutating the copy must not affect the live session.
	copy1.Entries = append(copy1.Entries, Entry{Role: "user", Content: "ghost"})
	copy1.Metadata["hack"] = "yes"

	live, ok := m.GetCopy("test")
	if !ok {
		t.Fatal("second GetCopy failed")
	}
	if len(live.Entries) != 2 {
		t.Fatalf("live session mutated by detached copy: %d entries", len(live.Entries))
	}
	if _, ok := live.Metadata["hack"]; ok {
		t.Error("live session metadata mutated by detached copy")
	}

	// SetMetadata on the manager must be visible to the next snapshot.
	if err := m.SetMetadata("test", "total_tokens", "42"); err != nil {
		t.Fatalf("SetMetadata: %v", err)
	}
	after, _ := m.GetCopy("test")
	if after.Metadata["total_tokens"] != "42" {
		t.Errorf("SetMetadata not visible in snapshot: %q", after.Metadata["total_tokens"])
	}
}
