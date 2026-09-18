package usage

import (
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLocalAggregationAttributionAndConcurrency(t *testing.T) {
	l := NewLocal(time.Unix(1, 0))
	var wg sync.WaitGroup
	for _, repo := range []string{"one", "two"} {
		wg.Add(1)
		go func(repo string) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				l.Observe(Delivery{RepositoryID: repo, AccountID: "a", Bytes: 3})
			}
		}(repo)
	}
	wg.Wait()
	l.Observe(Delivery{RepositoryID: "one", AccountID: "other", Bytes: 99, WriteFailed: true})
	if got := l.Account("a"); got.Bytes != 600 || got.Requests != 200 || got.WriteFailures != 0 {
		t.Fatalf("account=%+v", got)
	}
	if got := l.Repository("a", "one"); got.Bytes != 300 || got.Requests != 100 {
		t.Fatalf("repository=%+v", got)
	}
	if got := l.Account("other"); got.Bytes != 99 || got.WriteFailures != 1 {
		t.Fatalf("other=%+v", got)
	}
}

func TestMeasurementDependencyBoundary(t *testing.T) {
	// Inspect the real compiled package graph, not a helper's copy of the model.
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("dependency graph: %v %s", err, out)
	}
	for _, dependency := range strings.Fields(string(out)) {
		for _, forbidden := range []string{"github.com/eugen252009/tpahub/internal/payments", "github.com/eugen252009/tpahub/internal/commerce", "github.com/eugen252009/tpahub/internal/tpahubproduct", "github.com/eugen252009/tpahub/cmd/"} {
			if strings.HasPrefix(dependency, forbidden) {
				t.Fatalf("measurement depends on commercial/application policy: %s", dependency)
			}
		}
	}
}
