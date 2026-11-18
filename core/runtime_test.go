package base

import (
 "context"
 "errors"
 "sync"
 "sync/atomic"
 "testing"
 "time"
)

type mockComponent struct {
 start func() error
 health func() error
 close func() error
}
func (m *mockComponent) Start(_ context.Context,_ Env) error { if m.start!=nil{return m.start()};return nil }
func (m *mockComponent) Health(_ context.Context) error { if m.health!=nil{return m.health()};return nil }
func (m *mockComponent) Close(_ context.Context) error { if m.close!=nil{return m.close()};return nil }

func TestRuntimeStartupAndReverseClose(t *testing.T) {
 var order []string
 k:=NewKey("mock")
 reg:=func(name string) Registration {
  return Register(k,name,func()(Component,error){
   return &mockComponent{start:func()error{order=append(order,"start "+name);return nil},close:func()error{order=append(order,"close "+name);return nil}},nil
  })
 }
 rt,err:=New(context.Background(),Use(reg("one"),reg("two")))
 if err!=nil { t.Fatal(err) }
 if _,err:=rt.Get(k,"one");err!=nil {t.Fatal(err)}
 if !rt.Health(context.Background()).Healthy {t.Fatal("unexpected unhealthy")}
 if err:=rt.Close(context.Background());err!=nil {t.Fatal(err)}
 expected:=[]string{"start one","start two","close two","close one"}
 if len(order)!=len(expected){t.Fatalf("order: %v",order)}
 for i:=range expected {if order[i]!=expected[i]{t.Fatalf("order: %v",order)}}
 if _,err:=rt.Get(k,"one");!errors.Is(err,ErrNotRunning){t.Fatal(err)}
}
func TestStartFailureRollsBack(t *testing.T) {
 boom:=errors.New("start failed")
 var closed atomic.Int32
 k:=NewKey("mock")
 rt,err:=New(context.Background(),Use(
  Register(k,"first",func()(Component,error){return &mockComponent{close:func()error{closed.Add(1);return nil}},nil}),
  Register(k,"second",func()(Component,error){return &mockComponent{start:func()error{return boom}},nil}),
 ))
 if rt!=nil || !errors.Is(err,boom) || closed.Load()!=1 {t.Fatalf("rt=%v err=%v closed=%d",rt,err,closed.Load())}
}
func TestRegistrationValidationBeforeBuild(t *testing.T) {
 k:=NewKey("mock")
 var builds atomic.Int32
 build:=func()(Component,error){builds.Add(1);return &mockComponent{},nil}
 _,err:=New(context.Background(),Use(Register(k,"x",build),Register(k,"x",build)))
 if err==nil || builds.Load()!=0 {t.Fatalf("err=%v builds=%d",err,builds.Load())}
}
func TestConcurrentCloseOnlyOnce(t *testing.T) {
 k:=NewKey("mock")
 var count atomic.Int32
 rt,err:=New(context.Background(),Use(Register(k,"x",func()(Component,error){
  return &mockComponent{close:func()error{count.Add(1);time.Sleep(10*time.Millisecond);return nil}},nil
 })))
 if err!=nil {t.Fatal(err)}
 var wg sync.WaitGroup
 for i:=0;i<20;i++ {wg.Add(1);go func(){defer wg.Done();if err:=rt.Close(context.Background());err!=nil{t.Error(err)}}()}
 wg.Wait()
 if count.Load()!=1 {t.Fatalf("closed %d times",count.Load())}
}
func TestOptionalHealthAndConfigRejection(t *testing.T) {
 k:=NewKey("mock")
 unhealthy:=errors.New("offline")
 rt,err:=New(context.Background(),Use(Register(k,"cache",func()(Component,error){return &mockComponent{health:func()error{return unhealthy}},nil},WithRequired(false))))
 if err!=nil{t.Fatal(err)}
 report:=rt.Health(context.Background())
 if !report.Healthy || len(report.Components)!=1 || !errors.Is(report.Components[0].Err,unhealthy){t.Fatalf("report=%+v",report)}
 _=rt.Close(context.Background())
 _,err=New(context.Background(),WithConfig("app.conf"))
 if !errors.Is(err,ErrConfigUnsupported){t.Fatal(err)}
}
