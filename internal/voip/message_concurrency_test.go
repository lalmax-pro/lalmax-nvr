package voip

import (
	"strings"
	"sync"
	"testing"
)

func TestSIPIdentifiersConcurrent(t *testing.T) {
	const workers, perWorker = 32, 256
	results := make(chan string, workers*perWorker*2)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				results <- generateTag()
				branch := generateBranch()
				if !strings.HasPrefix(branch, "z9hG4bK-") {
					t.Errorf("invalid branch %q", branch)
				}
				results <- strings.TrimPrefix(branch, "z9hG4bK-")
			}
		}()
	}
	wg.Wait()
	close(results)
	seen := make(map[string]bool)
	for value := range results {
		if value == "" || seen[value] {
			t.Fatalf("empty or duplicate SIP identifier %q", value)
		}
		seen[value] = true
	}
}
