//go:build integration

// Run with a migrated database:
//
//	CONQUEST_TEST_POSTGRES_DSN=postgres://... go test -tags integration ./internal/repository/pg/
package pg

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/anuragxxd/conquest/internal/domain"
)

func testStores(t *testing.T) (*RunStore, *TaskStore) {
	t.Helper()
	dsn := os.Getenv("CONQUEST_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CONQUEST_TEST_POSTGRES_DSN not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return NewRunStore(pool), NewTaskStore(pool)
}

func TestDispatchQueue(t *testing.T) {
	ctx := context.Background()
	runs, tasks := testStores(t)
	run := domain.NewRun("NFL", "DSL2", "nextflow", "25.10.4", []byte(`{}`), "s3://b/o/", "v1", "s3://b/r/")
	if err := runs.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 0; i < 20; i++ {
		task := domain.NewTask(run.RunID)
		task.TESTaskID = task.TaskID
		if err := tasks.Create(ctx, task); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, task.TaskID)
	}

	// Concurrent dispatchers never claim the same task.
	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				claims, err := tasks.ClaimForDispatch(ctx, 3, time.Minute)
				if err != nil {
					t.Error(err)
					return
				}
				if len(claims) == 0 {
					return
				}
				mu.Lock()
				for _, c := range claims {
					seen[c.TaskID]++
					if c.Attempt != 1 {
						t.Errorf("attempt = %d, want 1", c.Attempt)
					}
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != len(ids) {
		t.Fatalf("claimed %d of %d tasks", len(seen), len(ids))
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("task %s claimed %d times", id, n)
		}
	}

	// A deferred task is claimed again after its delay, with the next attempt.
	if err := tasks.DeferDispatch(ctx, ids[0], time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := tasks.DeferDispatch(ctx, ids[1], time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	claims, err := tasks.ClaimForDispatch(ctx, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 || claims[0].TaskID != ids[0] || claims[0].Attempt != 2 {
		t.Fatalf("claims = %+v, want only %s at attempt 2", claims, ids[0])
	}

	// An expired lease frees the task; dispatched and cancelled tasks are not claimed.
	if _, err := tasks.pool.Exec(ctx, `UPDATE tasks SET dispatch_lease_until = NOW() - interval '1 second' WHERE task_id = $1`, ids[2]); err != nil {
		t.Fatal(err)
	}
	if err := tasks.SetBackendAssignment(ctx, ids[3], "", "conquest-task-x", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.pool.Exec(ctx, `UPDATE tasks SET dispatch_lease_until = NULL WHERE task_id = ANY($1)`, []string{ids[3], ids[4]}); err != nil {
		t.Fatal(err)
	}
	if err := tasks.UpdateState(ctx, ids[4], domain.StateQueued, domain.StateCanceled); err != nil {
		t.Fatal(err)
	}
	claims, err = tasks.ClaimForDispatch(ctx, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 || claims[0].TaskID != ids[2] {
		t.Fatalf("claims = %+v, want only %s (lease expired)", claims, ids[2])
	}

	// The reconciler sees dispatched tasks only.
	active, err := tasks.ListActiveTasks(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range active {
		if a.RunID == run.RunID && a.TaskID != ids[3] {
			t.Fatalf("reconciler listed undispatched task %s", a.TaskID)
		}
	}
}
