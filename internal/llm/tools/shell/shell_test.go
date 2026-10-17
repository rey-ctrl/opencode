package shell

import (
	"os"
	"sync"
	"testing"
)

func TestShellPool_IsolationAndRace(t *testing.T) {
	tmpDir := os.TempDir()

	// Verify isolation across sessions
	s1 := GetShellForSession("session-1", tmpDir)
	s2 := GetShellForSession("session-2", tmpDir)
	if s1 == s2 {
		t.Fatalf("expected different shell instances for different sessions")
	}

	// Verify idempotency for same session
	s1Again := GetShellForSession("session-1", tmpDir)
	if s1 != s1Again {
		t.Fatalf("expected same shell instance for same session")
	}

	// Concurrent access test
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			s := GetShellForSession("session-concurrent", tmpDir)
			if s == nil || !s.isAlive.Load() {
				t.Errorf("shell should be alive")
			}
		}(i)
	}
	wg.Wait()

	CloseShellForSession("session-1")
	CloseShellForSession("session-2")
	CloseShellForSession("session-concurrent")
}
