package base

import (
 "errors"
 "reflect"
 "testing"
)

func TestConfigDecodeDefaultsAndTags(t *testing.T) {
 cfg:=snapshotConfig{root:map[string]any{"app":map[string]any{"name":"kurmon","workers":int64(4)}}}
 type App struct {
  Name string `conf:"name"`
  Workers int `conf:"workers"`
  Enabled bool `conf:"enabled"`
 }
 dst:=App{Enabled:true}
 if err:=cfg.Decode("app",&dst);err!=nil{t.Fatal(err)}
 if dst.Name!="kurmon"||dst.Workers!=4||!dst.Enabled{t.Fatalf("%+v",dst)}
}

func TestConfigDecodeAtomicOnFailure(t *testing.T) {
 cfg:=snapshotConfig{root:map[string]any{"app":map[string]any{"name":"new","count":"not a number"}}}
 type App struct{ Name string `conf:"name"`; Count int `conf:"count"` }
 dst:=App{Name:"old",Count:5}
 err:=cfg.Decode("app",&dst)
 if !errors.Is(err,ErrConfigType){t.Fatal(err)}
 if dst.Name!="old"||dst.Count!=5{t.Fatalf("partially modified: %+v",dst)}
}

func TestConfigDecodeIsolation(t *testing.T) {
 cfg:=snapshotConfig{root:map[string]any{"items":[]any{map[string]any{"name":"original"}}}}
 type Item struct{ Name string `conf:"name"` }
 var first,second []Item
 if err:=cfg.Decode("items",&first);err!=nil{t.Fatal(err)}
 if err:=cfg.Decode("items",&second);err!=nil{t.Fatal(err)}
 first[0].Name="changed"
 if second[0].Name!="original"{t.Fatal("decode leaked mutable reference")}
}

func TestConfigDecodeMissingUnknownAndOverflow(t *testing.T) {
 cfg:=snapshotConfig{root:map[string]any{"a":map[string]any{"unexpected":true},"n":int64(128)}}
 var x struct{ Known bool `conf:"known"` }
 if err:=cfg.Decode("missing",&x);!errors.Is(err,ErrConfigNotFound){t.Fatal(err)}
 if err:=cfg.Decode("a",&x);!errors.Is(err,ErrConfigType){t.Fatal(err)}
 var small int8
 if err:=cfg.Decode("n",&small);!errors.Is(err,ErrConfigType){t.Fatal(err)}
}

func TestConfigDecodeNullAndMap(t *testing.T) {
 cfg:=snapshotConfig{root:map[string]any{"data":map[string]any{"a":int64(1)},"none":nil}}
 var m map[string]int
 if err:=cfg.Decode("data",&m);err!=nil{t.Fatal(err)}
 if !reflect.DeepEqual(m,map[string]int{"a":1}){t.Fatalf("%v",m)}
 var ptr *string
 if err:=cfg.Decode("none",&ptr);err!=nil{t.Fatal(err)}
 if ptr!=nil{t.Fatal("expected nil")}
}
