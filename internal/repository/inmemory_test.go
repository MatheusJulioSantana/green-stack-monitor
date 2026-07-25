package repository_test

import (
	"context"
	"sync"
	"testing"

	"github.com/matheusjuliosantana/green-stack-monitor/internal/repository"
	"github.com/matheusjuliosantana/green-stack-monitor/pkg/domain"
)

func TestInMemoryEcoRepositoryAggregates(t *testing.T) {
	r := &repository.InMemoryEcoRepository{}
	ctx := context.Background()

	traces := []domain.RequestTrace{
		{CacheHit: false},
		{CacheHit: true, CO2Saved: 0.001},
		{CacheHit: true, CO2Saved: 0.002},
	}
	for _, trace := range traces {
		if err := r.Save(ctx, trace); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	metrics, err := r.Aggregates(ctx)
	if err != nil {
		t.Fatalf("Aggregates: %v", err)
	}
	if metrics.Hits != 2 || metrics.Misses != 1 {
		t.Errorf("Hits/Misses = %d/%d, want 2/1", metrics.Hits, metrics.Misses)
	}
	if metrics.TotalSaved != 0.003 {
		t.Errorf("TotalSaved = %.6f, want 0.003000", metrics.TotalSaved)
	}
}

func TestInMemoryEcoRepositorySnapshotIsCopy(t *testing.T) {
	r := &repository.InMemoryEcoRepository{}
	if err := r.Save(context.Background(), domain.RequestTrace{TraceID: "original"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	snapshot := r.Snapshot()
	snapshot[0].TraceID = "changed"

	if got := r.Snapshot()[0].TraceID; got != "original" {
		t.Errorf("repository trace = %q, want original", got)
	}
}

func TestInMemoryEcoRepositoryConcurrentSave(t *testing.T) {
	r := &repository.InMemoryEcoRepository{}
	const requests = 100

	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func(hit bool) {
			defer wg.Done()
			_ = r.Save(context.Background(), domain.RequestTrace{CacheHit: hit, CO2Saved: 0.001})
		}(i%2 == 0)
	}
	wg.Wait()

	metrics, err := r.Aggregates(context.Background())
	if err != nil {
		t.Fatalf("Aggregates: %v", err)
	}
	if metrics.Hits+metrics.Misses != requests {
		t.Errorf("total requests = %d, want %d", metrics.Hits+metrics.Misses, requests)
	}
	if got := len(r.Snapshot()); got != requests {
		t.Errorf("snapshot length = %d, want %d", got, requests)
	}
}
