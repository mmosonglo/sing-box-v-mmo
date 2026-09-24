package butler

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestSurveySingBoxMemoryCost(t *testing.T) {
	costKB, count := surveySingBoxMemoryCost()
	if costKB < 3500 {
		t.Fatalf("expected estimated memory cost at least 3500 KB, got %d", costKB)
	}
	t.Logf("survey result: count=%d, cost=%d KB", count, costKB)
}

func TestAcquireStartupGateSequential(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var order []int
	var mu sync.Mutex

	var wg sync.WaitGroup
	for i := 1; i <= 3; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			unlock, err := AcquireStartupGate(ctx)
			if err != nil {
				t.Errorf("worker %d failed to acquire gate: %v", id, err)
				return
			}
			defer unlock()

			mu.Lock()
			order = append(order, id)
			mu.Unlock()

			// Giả lập thời gian nạp cấu hình 50ms
			time.Sleep(50 * time.Millisecond)
		}(i)
	}

	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 3 {
		t.Fatalf("expected all 3 workers to pass startup gate sequentially, got %d", len(order))
	}
	t.Logf("Sequential startup order: %v", order)
}
