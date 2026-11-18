package base

import (
	"context"
	"errors"
	"testing"
)

type lookupProbe struct {
	onStart func(Env) error
	closed *int
}
func (p *lookupProbe) Start(_ context.Context, e Env) error { if p.onStart != nil { return p.onStart(e) }; return nil }
func (p *lookupProbe) Health(context.Context) error { return nil }
func (p *lookupProbe) Close(context.Context) error { if p.closed != nil { *p.closed++ }; return nil }

func TestEnvLookupPriorOnlyAndRollback(t *testing.T) {
	a := NewKey("first")
	b := NewKey("second")
	var first Component
	closed := 0
	_, err := New(context.Background(), Use(
		Register(a, "default", func() (Component,error) {
			first = &lookupProbe{closed: &closed}
			return first,nil
		}),
		Register(b, "default", func() (Component,error) {
			return &lookupProbe{onStart: func(env Env) error {
				got, err := env.Lookup(a, "default")
				if err != nil || got != first { t.Fatalf("prior lookup: %v", err) }
				if _, err := env.Lookup(b, "default"); !errors.Is(err, ErrNotRegistered) { t.Fatalf("self lookup: %v", err) }
				return errors.New("intentional failure")
			}},nil
		}),
	))
	if err == nil || closed != 1 { t.Fatalf("expected startup failure and rollback, err=%v closed=%d",err,closed) }
}

func TestEnvLookupCannotSeeFuture(t *testing.T) {
	a,b := NewKey("alpha"),NewKey("beta")
	rt,err := New(context.Background(),Use(
		Register(a,"default",func()(Component,error){return &lookupProbe{onStart:func(env Env)error{
			if _,err:=env.Lookup(b,"default"); !errors.Is(err,ErrNotRegistered){t.Fatalf("future lookup: %v",err)}
			return nil
		}},nil}),
		Register(b,"default",func()(Component,error){return &lookupProbe{},nil}),
	))
	if err!=nil { t.Fatal(err) }
	if err:=rt.Close(context.Background());err!=nil{t.Fatal(err)}
}
