package agentapp

import (
	"context"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
)

type retryStubSettings struct {
	ints map[string]int64
	err  error
}

func (s *retryStubSettings) Int(_ context.Context, key string) (int64, error) {
	if s.err != nil {
		return 0, s.err
	}
	value, ok := s.ints[key]
	if !ok {
		return 0, context.Canceled
	}
	return value, nil
}

func (s *retryStubSettings) Float(_ context.Context, key string) (float64, error) {
	return 0, context.Canceled
}

type retryEventPublisher struct {
	events []eventingapp.Event
}

func (p *retryEventPublisher) Publish(_ context.Context, event eventingapp.Event) error {
	p.events = append(p.events, event)
	return nil
}

func newRetryRunner(settings *retryStubSettings) (*Runner, *retryEventPublisher) {
	publisher := &retryEventPublisher{}
	runner := &Runner{events: publisher}
	if settings != nil {
		runner.settings = settings
	}
	return runner, publisher
}

func retryState() *runState {
	return &runState{
		item: agent.WorkItem{VersionID: 7},
		run:  agent.AgentRun{ID: 11},
	}
}

// TestToolRetryMaxFromSettings pins the W1-2 wiring of tool.retry_max: the
// runner reads the setting and zero is a valid value (retries disabled), unlike
// the positive-only intSetting helper.
func TestToolRetryMaxFromSettings(t *testing.T) {
	cases := []struct {
		name     string
		settings *retryStubSettings
		want     int
	}{
		{name: "configured", settings: &retryStubSettings{ints: map[string]int64{"tool.retry_max": 3}}, want: 3},
		{name: "zero disables", settings: &retryStubSettings{ints: map[string]int64{"tool.retry_max": 0}}, want: 0},
		{name: "missing falls back", settings: &retryStubSettings{ints: map[string]int64{}}, want: 2},
		{name: "unwired falls back", settings: nil, want: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner, _ := newRetryRunner(tc.settings)
			if got := runner.toolRetryMax(); got != tc.want {
				t.Fatalf("toolRetryMax = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestExecuteToolWithRetry pins the minimal deterministic retry loop: a
// retryable typed tool error is retried immediately up to the configured budget
// and every retry publishes tool.retry with the attempt and error. A zero budget
// and a non-retryable error execute exactly once.
func TestExecuteToolWithRetry(t *testing.T) {
	t.Run("retryable error is retried", func(t *testing.T) {
		runner, publisher := newRetryRunner(&retryStubSettings{ints: map[string]int64{"tool.retry_max": 2}})
		attempts := 0
		payload, err := runner.executeToolWithRetry(context.Background(), retryState(), "demo", func() (any, error) {
			attempts++
			if attempts <= 2 {
				return nil, &tools.Error{Code: "unavailable", Message: "executor unavailable", Retryable: true}
			}
			return "ok", nil
		})
		if err != nil {
			t.Fatalf("executeToolWithRetry err = %v", err)
		}
		if payload != "ok" || attempts != 3 {
			t.Fatalf("payload/attempts = %v/%d, want ok/3", payload, attempts)
		}
		if len(publisher.events) != 2 {
			t.Fatalf("tool.retry events = %d, want 2", len(publisher.events))
		}
		for index, event := range publisher.events {
			if event.Type != "tool.retry" {
				t.Fatalf("event %d type = %q, want tool.retry", index, event.Type)
			}
			if got := event.Payload["attempt"]; got != index+1 {
				t.Fatalf("event %d attempt = %v, want %d", index, got, index+1)
			}
			message, _ := event.Payload["error"].(string)
			if !strings.Contains(message, "executor unavailable") {
				t.Fatalf("event %d error = %q, want the tool error text", index, message)
			}
		}
	})

	t.Run("zero disables retries", func(t *testing.T) {
		runner, publisher := newRetryRunner(&retryStubSettings{ints: map[string]int64{"tool.retry_max": 0}})
		attempts := 0
		_, err := runner.executeToolWithRetry(context.Background(), retryState(), "demo", func() (any, error) {
			attempts++
			return nil, &tools.Error{Code: "unavailable", Message: "executor unavailable", Retryable: true}
		})
		if err == nil {
			t.Fatal("executeToolWithRetry err = nil, want the retryable tool error")
		}
		if attempts != 1 {
			t.Fatalf("attempts = %d, want 1 with retries disabled", attempts)
		}
		if len(publisher.events) != 0 {
			t.Fatalf("tool.retry events = %d, want 0", len(publisher.events))
		}
	})

	t.Run("non-retryable error is not retried", func(t *testing.T) {
		runner, publisher := newRetryRunner(&retryStubSettings{ints: map[string]int64{"tool.retry_max": 3}})
		attempts := 0
		_, err := runner.executeToolWithRetry(context.Background(), retryState(), "demo", func() (any, error) {
			attempts++
			return nil, &tools.Error{Code: "invalid_argument", Message: "bad field", Retryable: false}
		})
		if err == nil {
			t.Fatal("executeToolWithRetry err = nil, want the typed tool error")
		}
		if attempts != 1 {
			t.Fatalf("attempts = %d, want 1 for a non-retryable error", attempts)
		}
		if len(publisher.events) != 0 {
			t.Fatalf("tool.retry events = %d, want 0", len(publisher.events))
		}
	})
}
