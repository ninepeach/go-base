package base

import (
 "context"
 "errors"
 "fmt"
 "log/slog"
 "regexp"
 "time"
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
var ErrConfigNotFound = errors.New("base: config path not found")
var ErrConfigType = errors.New("base: config type mismatch")
var ErrInvalidDestination = errors.New("base: invalid decode destination")
var ErrConfigUnsupported = errors.New("base: config file loading not implemented")

type emptyConfig struct{}
func (emptyConfig) Decode(path string, dst any) error { return fmt.Errorf("%w: %q", ErrConfigNotFound, path) }

type componentEnv struct { name string; config Config; logger *slog.Logger }
func (e componentEnv) Name() string { return e.name }
func (e componentEnv) Config() Config { return e.config }
func (e componentEnv) Logger() *slog.Logger { return e.logger }

// New builds and starts components in registration order.
// File-backed configuration is intentionally rejected until go-conf integration lands.
func New(ctx context.Context, opts ...Option) (*Runtime, error) {
 if ctx == nil { return nil, errors.New("base: nil context") }
 var o options
 for _, opt := range opts {
  if opt == nil { return nil, errors.New("base: nil option") }
  if err := opt.apply(&o); err != nil { return nil, err }
 }
 if o.configPath != "" { return nil, fmt.Errorf("%w: %s", ErrConfigUnsupported, o.configPath) }
 seen := make(map[*privateToken]map[string]bool)
 providers := make(map[string]*privateToken)
 for _, r := range o.registrations {
  if r.key.token == nil || !namePattern.MatchString(r.key.provider) || !namePattern.MatchString(r.instance) || r.build == nil {
   return nil, fmt.Errorf("base: invalid registration %q/%q", r.key.provider, r.instance)
  }
  if prev, ok := providers[r.key.provider]; ok && prev != r.key.token {
   return nil, fmt.Errorf("base: provider name collision: %s", r.key.provider)
  }
  providers[r.key.provider] = r.key.token
  if seen[r.key.token] == nil { seen[r.key.token] = make(map[string]bool) }
  if seen[r.key.token][r.instance] { return nil, fmt.Errorf("base: duplicate registration: %s/%s", r.key.provider, r.instance) }
  seen[r.key.token][r.instance] = true
 }
 rt := &Runtime{state: StateRunning, config: snapshotConfig{root: map[string]any{}}, logger: slog.Default(), done: make(chan struct{})}
 for _, r := range o.registrations {
  if err := ctx.Err(); err != nil { return nil, errors.Join(err, rollback(rt.components)) }
  name := r.key.provider + "/" + r.instance
  c, err := r.build()
  if err != nil { return nil, errors.Join(fmt.Errorf("base: build %s: %w", name, err), rollback(rt.components)) }
  if c == nil { return nil, errors.Join(fmt.Errorf("base: build %s: nil component", name), rollback(rt.components)) }
  env := componentEnv{name: name, config: snapshotConfig{root: map[string]any{}, prefix: "components."+r.key.provider+"."+r.instance}, logger: rt.logger.With("component", name)}
  if err := c.Start(ctx, env); err != nil {
   return nil, errors.Join(fmt.Errorf("base: start %s: %w", name, err), rollback(rt.components))
  }
  rt.components = append(rt.components, runningComponent{name:name, key:r.key, instance:r.instance, required:r.required, component:c})
 }
 return rt,nil
}

func rollback(items []runningComponent) error {
 ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
 defer cancel()
 return closeComponents(ctx,items)
}
func closeComponents(ctx context.Context, items []runningComponent) error {
 var errs []error
 for i:=len(items)-1;i>=0;i-- {
  if err:=items[i].component.Close(ctx);err!=nil { errs=append(errs,fmt.Errorf("base: close %s: %w",items[i].name,err)) }
 }
 if err:=ctx.Err();err!=nil { errs=append(errs,err) }
 return errors.Join(errs...)
}

func (rt *Runtime) Get(key Key, instance string) (Component,error) {
 rt.mu.RLock()
 defer rt.mu.RUnlock()
 if rt.state != StateRunning { return nil,ErrNotRunning }
 for _, item := range rt.components {
  if item.key.token == key.token && key.token != nil && item.instance == instance { return item.component,nil }
 }
 return nil,ErrNotRegistered
}

func (rt *Runtime) Health(ctx context.Context) HealthReport {
 rt.mu.RLock()
 state := rt.state
 items := append([]runningComponent(nil),rt.components...)
 rt.mu.RUnlock()
 report := HealthReport{State:state, Healthy:state==StateRunning, Components:make([]ComponentHealth,0,len(items))}
 for _, item := range items {
  var err error
  if state != StateRunning { err=ErrNotRunning
  } else if e:=ctx.Err();e!=nil { err=e
  } else { err=item.component.Health(ctx) }
  report.Components=append(report.Components,ComponentHealth{Name:item.name,Required:item.required,Err:err})
  if item.required && err!=nil { report.Healthy=false }
 }
 if ctx.Err()!=nil { report.Healthy=false }
 return report
}

func (rt *Runtime) Close(ctx context.Context) error {
 rt.mu.Lock()
 if rt.state != StateRunning {
  done := rt.done
  rt.mu.Unlock()
  select {
  case <-done:
   rt.mu.RLock(); defer rt.mu.RUnlock()
   return rt.closeErr
  case <-ctx.Done(): return ctx.Err()
  }
 }
 rt.state=StateClosing
 items:=append([]runningComponent(nil),rt.components...)
 rt.mu.Unlock()
 err:=closeComponents(ctx,items)
 rt.mu.Lock()
 rt.closeErr=err
 rt.state=StateClosed
 close(rt.done)
 rt.mu.Unlock()
 return err
}
