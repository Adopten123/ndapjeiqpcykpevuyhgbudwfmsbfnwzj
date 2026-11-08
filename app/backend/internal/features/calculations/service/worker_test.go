package service

import (
	"context"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"rzdarctica.local/backend/internal/features/calculations"
)

type workerStore struct {
	calculation   calculations.Calculation
	proceed       bool
	saves         int
	failureCalled bool
	terminal      bool
}

func (s *workerStore) BeginAttempt(context.Context, string) (calculations.Calculation, bool, error) {
	return s.calculation, s.proceed, nil
}
func (s *workerStore) Succeed(context.Context, string, []byte) error { s.saves++; return nil }
func (s *workerStore) RetryOrFail(_ context.Context, _ string, terminal bool, _ string) error {
	s.failureCalled = true
	s.terminal = terminal
	return nil
}

type fakeSolver struct {
	response calculations.ComputeResponse
	err      error
	calls    int
}

func (s *fakeSolver) Solve(context.Context, calculations.ComputeRequest) (calculations.ComputeResponse, error) {
	s.calls++
	return s.response, s.err
}
func job(attempt int) *river.Job[calculations.JobArgs] {
	return &river.Job[calculations.JobArgs]{JobRow: &rivertype.JobRow{Attempt: attempt, MaxAttempts: 3}, Args: calculations.JobArgs{CalculationID: "app"}}
}

func TestWorkerTerminalDeliveryIsNoop(t *testing.T) {
	s := &workerStore{proceed: false}
	solver := &fakeSolver{}
	if err := NewWorker(s, solver).Work(context.Background(), job(2)); err != nil || solver.calls != 0 || s.saves != 0 {
		t.Fatal("terminal delivery repeated work")
	}
}

func TestWorkerFailureLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name      string
		attempt   int
		err       error
		terminal  bool
		wantRetry bool
	}{
		{"retryable", 1, calculations.ErrCompute, false, true},
		{"last-attempt", 3, calculations.ErrCompute, true, false},
		{"permanent-input-rejection", 1, &ComputeHTTPError{Status: 422}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, r := validBoundResult()
			s := &workerStore{calculation: c, proceed: true}
			solver := &fakeSolver{response: r, err: tc.err}
			err := NewWorker(s, solver).Work(context.Background(), job(tc.attempt))
			if s.saves != 0 || !s.failureCalled || s.terminal != tc.terminal || (err != nil) != tc.wantRetry {
				t.Fatalf("wrong lifecycle: terminal %v retry %v", s.terminal, err)
			}
		})
	}
}

func TestWorkerSavesOnlyValidatedSuccess(t *testing.T) {
	c, r := validBoundResult()
	s := &workerStore{calculation: c, proceed: true}
	solver := &fakeSolver{response: r}
	if err := NewWorker(s, solver).Work(context.Background(), job(1)); err != nil || s.saves != 1 || s.failureCalled {
		t.Fatal("valid result not saved")
	}
	s = &workerStore{calculation: c, proceed: true}
	solver.response.RequestID = "other"
	if err := NewWorker(s, solver).Work(context.Background(), job(1)); err != nil || s.saves != 0 || !s.failureCalled || !s.terminal {
		t.Fatal("unbound result saved")
	}
	_, malformed := validBoundResult()
	mutateResult(&malformed, func(body map[string]any) { body["candidate"] = map[string]any{} })
	s = &workerStore{calculation: c, proceed: true}
	solver = &fakeSolver{response: malformed}
	if err := NewWorker(s, solver).Work(context.Background(), job(1)); err != nil || s.saves != 0 || !s.terminal {
		t.Fatal("malformed successful RPC must not be stored as a completed result")
	}
}
