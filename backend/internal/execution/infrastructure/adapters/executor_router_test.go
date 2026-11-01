package adapters

import (
	"context"
	"testing"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	executorinfra "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/executor"
)

type fakeExecutorLookup struct {
	rows map[int64]execution.ExecutorInstance
}

func (f *fakeExecutorLookup) Get(_ context.Context, id int64) (execution.ExecutorInstance, error) {
	row, ok := f.rows[id]
	if !ok {
		return execution.ExecutorInstance{}, execution.ErrNotFound
	}
	return row, nil
}

func (f *fakeExecutorLookup) List(context.Context) ([]execution.ExecutorInstance, error) {
	rows := make([]execution.ExecutorInstance, 0, len(f.rows))
	for _, row := range f.rows {
		rows = append(rows, row)
	}
	return rows, nil
}

func TestExecutorRouterRoutesByExecutorID(t *testing.T) {
	resolver := executorinfra.NewResolver("token", time.Second)
	lookup := &fakeExecutorLookup{rows: map[int64]execution.ExecutorInstance{
		1: {ID: 1, Endpoint: "http://127.0.0.1:14001", Status: execution.ExecutorActive},
		2: {ID: 2, Endpoint: "http://127.0.0.1:14002", Status: execution.ExecutorActive},
	}}
	router := NewExecutorRouter(resolver, lookup, "http://127.0.0.1:14000")

	first, err := router.clientFor(context.Background(), 1)
	if err != nil {
		t.Fatalf("clientFor(1): %v", err)
	}
	second, err := router.clientFor(context.Background(), 2)
	if err != nil {
		t.Fatalf("clientFor(2): %v", err)
	}
	if first == second {
		t.Fatal("distinct executors share one client")
	}
	fallback, err := router.clientFor(context.Background(), 0)
	if err != nil {
		t.Fatalf("clientFor(0): %v", err)
	}
	if fallback != resolver.Client("http://127.0.0.1:14000") {
		t.Fatal("executor 0 did not fall back to the primary endpoint")
	}
	unknown, err := router.clientFor(context.Background(), 99)
	if err != nil {
		t.Fatalf("clientFor(unknown): %v", err)
	}
	if unknown != fallback {
		t.Fatal("unknown executor did not fall back to the primary endpoint")
	}
}

func TestExecutorRouterListsEveryLiveEndpoint(t *testing.T) {
	resolver := executorinfra.NewResolver("token", time.Second)
	lookup := &fakeExecutorLookup{rows: map[int64]execution.ExecutorInstance{
		1: {ID: 1, Endpoint: "http://127.0.0.1:14001", Status: execution.ExecutorActive},
		2: {ID: 2, Endpoint: "http://127.0.0.1:14002", Status: execution.ExecutorDraining},
		3: {ID: 3, Endpoint: "http://127.0.0.1:14003", Status: execution.ExecutorOffline},
	}}
	router := NewExecutorRouter(resolver, lookup, "http://127.0.0.1:14000")
	clients, err := router.clients(context.Background())
	if err != nil {
		t.Fatalf("clients: %v", err)
	}
	if len(clients) != 3 {
		t.Fatalf("clients = %d, want 3 (two live + primary)", len(clients))
	}
	ids := map[int64]bool{}
	for _, client := range clients {
		ids[client.ExecutorID] = true
	}
	if !ids[1] || !ids[2] || !ids[0] {
		t.Fatalf("routed ids = %v, want 0/1/2", ids)
	}
}
