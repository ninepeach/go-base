package base

import (
 "testing"
)

func TestKeysHaveDistinctIdentity(t *testing.T) {
 a := NewKey("postgres")
 b := NewKey("postgres")
 if a.token == nil || b.token == nil { t.Fatal("key identity missing") }
 if a.token == b.token { t.Fatal("different keys share identity") }
 if a.provider != "postgres" { t.Fatalf("unexpected provider: %q", a.provider) }
}

func TestRegistrationDefaultsToRequired(t *testing.T) {
 key := NewKey("postgres")
 build := func() (Component,error) { return nil,nil }
 required := Register(key,"primary",build)
 optional := Register(key,"readonly",build,WithRequired(false))
 if !required.required { t.Fatal("registration must be required by default") }
 if optional.required { t.Fatal("optional registration should not be required") }
}

func TestUsePreservesRegistrationOrder(t *testing.T) {
 key := NewKey("postgres")
 a := Register(key,"primary",func()(Component,error){return nil,nil})
 b := Register(key,"readonly",func()(Component,error){return nil,nil})
 var o options
 if err:=Use(a,b).apply(&o);err!=nil { t.Fatal(err) }
 if len(o.registrations)!=2 || o.registrations[0].instance!="primary" || o.registrations[1].instance!="readonly" {
  t.Fatal("registration order changed")
 }
}
