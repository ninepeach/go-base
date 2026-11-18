package base

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProviderNamespaceCollision(t *testing.T) {
	a, b := NewKey("postgres"), NewKey("postgres")
	var builds atomic.Int32
	build := func() (Component, error) {
		builds.Add(1)
		return &mockComponent{}, nil
	}
	_, err := New(context.Background(), Use(
		Register(a, "primary", build),
		Register(b, "readonly", build),
	))
	if err == nil || builds.Load() != 0 {
		t.Fatalf("collision should fail before building: err=%v builds=%d", err, builds.Load())
	}
}

func TestCloseAggregatesErrorsAndDoesNotRetry(t *testing.T) {
	k := NewKey("mock")
	firstErr := errors.New("first close failed")
	secondErr := errors.New("second close failed")
	var firstCalls, secondCalls atomic.Int32
	rt, err := New(context.Background(), Use(
		Register(k, "first", func() (Component, error) {
			return &mockComponent{close: func() error {
				firstCalls.Add(1)
				return firstErr
			}}, nil
		}),
		Register(k, "second", func() (Component, error) {
			return &mockComponent{close: func() error {
				secondCalls.Add(1)
				return secondErr
			}}, nil
		}),
	))
	if err != nil {
		t.Fatal(err)
	}
	result := rt.Close(context.Background())
	if !errors.Is(result, firstErr) || !errors.Is(result, secondErr) {
		t.Fatalf("close lost errors: %v", result)
	}
	again := rt.Close(context.Background())
	if !errors.Is(again, firstErr) || !errors.Is(again, secondErr) {
		t.Fatalf("repeat close lost errors: %v", again)
	}
	if firstCalls.Load() != 1 || secondCalls.Load() != 1 {
		t.Fatalf("close retried: %d %d", firstCalls.Load(), secondCalls.Load())
	}
}

func TestCloseWaiterCanCancel(t *testing.T) {
	k := NewKey("mock")
	entered := make(chan struct{})
	release := make(chan struct{})
	rt, err := New(context.Background(), Use(Register(k, "slow", func() (Component, error) {
		return &mockComponent{close: func() error {
			close(entered)
			<-release
			return nil
		}}, nil
	})))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := rt.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := rt.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiter returned %v", err)
	}
	close(release)
	wg.Wait()
	if err := rt.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestHealthAfterCloseDoesNotProbe(t *testing.T) {
	k := NewKey("mock")
	var probes atomic.Int32
	rt, err := New(context.Background(), Use(Register(k, "primary", func() (Component, error) {
		return &mockComponent{health: func() error {
			probes.Add(1)
			return nil
		}}, nil
	})))
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	report := rt.Health(context.Background())
	if report.Healthy || report.State != StateClosed || !errors.Is(report.Err, ErrClosed) || probes.Load() != 0 {
		t.Fatalf("invalid closed health: %+v probes=%d", report, probes.Load())
	}
}

func TestHealthDuringClosingReturnsErrClosedWithoutProbing(t *testing.T) {
	k := NewKey("mock")
	entered := make(chan struct{})
	release := make(chan struct{})
	var probes atomic.Int32
	rt, err := New(context.Background(), Use(Register(k, "blocking", func() (Component, error) {
		return &mockComponent{
			health: func() error {
				probes.Add(1)
				return nil
			},
			close: func() error {
				close(entered)
				<-release
				return nil
			},
		}, nil
	})))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- rt.Close(context.Background()) }()
	<-entered
	report := rt.Health(context.Background())
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if report.State != StateClosing || report.Healthy || !errors.Is(report.Err, ErrClosed) {
		t.Fatalf("unexpected closing health: %+v", report)
	}
	if probes.Load() != 0 {
		t.Fatalf("health called during closing: %d", probes.Load())
	}
}
