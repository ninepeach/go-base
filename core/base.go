package base

import (
 "context"
 "errors"
 "log/slog"
 "sync"
)

var (
 ErrNotRegistered = errors.New("base: component not registered")
 ErrNotRunning = errors.New("base: runtime not running")
 ErrClosed = errors.New("base: runtime closed")
)

type Component interface {
 Start(context.Context, Env) error
 Health(context.Context) error
 Close(context.Context) error
}
type Env interface {
 Name() string
 Config() Config
 Logger() *slog.Logger
}
type Config interface { Decode(path string, dst any) error }
type State string
const (
 StateRunning State = "running"
 StateClosing State = "closing"
 StateClosed State = "closed"
)
type ComponentHealth struct {
 Name string
 Required bool
 Err error
}
type HealthReport struct {
 State State
 Err error
 Components []ComponentHealth
 Healthy bool
}
type privateToken struct { marker byte }
type Key struct {
 token *privateToken
 provider string
}
func NewKey(provider string) Key { return Key{token: &privateToken{marker: 1}, provider: provider} }
type Registration struct {
 key Key
 instance string
 build func() (Component, error)
 required bool
}
type registrationOptions struct { required bool }
type RegistrationOption interface { applyRegistration(*registrationOptions) }
type registrationOptionFunc func(*registrationOptions)
func (f registrationOptionFunc) applyRegistration(o *registrationOptions) { f(o) }
func WithRequired(required bool) RegistrationOption {
 return registrationOptionFunc(func(o *registrationOptions) { o.required = required })
}
func Register(key Key, instance string, build func() (Component,error), opts ...RegistrationOption) Registration {
 o := registrationOptions{required:true}
 for _, opt := range opts { if opt != nil { opt.applyRegistration(&o) } }
 return Registration{key:key,instance:instance,build:build,required:o.required}
}
type options struct {
 configPath string
 registrations []Registration
}
type Option interface { apply(*options) error }
type optionFunc func(*options) error
func (f optionFunc) apply(o *options) error { return f(o) }
func WithConfig(path string) Option {
 return optionFunc(func(o *options) error { o.configPath=path;return nil })
}
func Use(registrations ...Registration) Option {
 return optionFunc(func(o *options) error { o.registrations=append(o.registrations,registrations...);return nil })
}
type runningComponent struct {
 name string
 key Key
 instance string
 required bool
 component Component
}
type Runtime struct {
 mu sync.RWMutex
 state State
 components []runningComponent
 logger *slog.Logger
 config Config
 done chan struct{}
 closeErr error
}
func (rt *Runtime) Logger() *slog.Logger { return rt.logger }
func (rt *Runtime) Config() Config { return rt.config }
