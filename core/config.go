package base

import (
 "fmt"
 "math"
 "reflect"
 "strings"
)

// snapshotConfig is an immutable configuration view. It never exposes the source map.
type snapshotConfig struct {
 root map[string]any
 prefix string
}

func (c snapshotConfig) child(path string) snapshotConfig {
 if c.prefix == "" { return snapshotConfig{root:c.root,prefix:path} }
 return snapshotConfig{root:c.root,prefix:c.prefix+"."+path}
}

func (c snapshotConfig) Decode(path string, dst any) error {
 v := reflect.ValueOf(dst)
 if !v.IsValid() || v.Kind()!=reflect.Pointer || v.IsNil() {
  return fmt.Errorf("%w: destination must be non-nil pointer",ErrInvalidDestination)
 }
 full:=c.prefix
 if path!="" {
  for _, part:=range strings.Split(path,".") {
   if part=="" { return fmt.Errorf("%w: invalid path",ErrConfigNotFound) }
  }
  if full!="" {full+="."}
  full+=path
 }
 var source any=c.root
 if full!="" {
  for _,part:=range strings.Split(full,".") {
   m,ok:=source.(map[string]any)
   if !ok {return fmt.Errorf("%w: %s is not an object",ErrConfigType,full)}
   value,found:=m[part]
   if !found {return fmt.Errorf("%w: %s",ErrConfigNotFound,full)}
   source=value
  }
 }
 // Clone the pre-populated destination first. The final Set is the only mutation.
 cloned,err:=cloneValue(v.Elem(),full)
 if err!=nil {return err}
 if err:=decodeInto(cloned,source,full);err!=nil{return err}
 v.Elem().Set(cloned)
 return nil
}

func cloneValue(v reflect.Value, path string) (reflect.Value,error) {
 out:=reflect.New(v.Type()).Elem()
 switch v.Kind() {
 case reflect.Pointer:
  if !v.IsNil() {
   child,err:=cloneValue(v.Elem(),path);if err!=nil{return out,err}
   out.Set(reflect.New(v.Type().Elem()));out.Elem().Set(child)
  }
 case reflect.Interface:
  if !v.IsNil() {return out,fmt.Errorf("%w: existing interface at %s",ErrInvalidDestination,path)}
 case reflect.Map:
  if !v.IsNil() {
   if v.Type().Key().Kind()!=reflect.String{return out,fmt.Errorf("%w: map key at %s",ErrInvalidDestination,path)}
   out.Set(reflect.MakeMapWithSize(v.Type(),v.Len()))
   iter:=v.MapRange()
   for iter.Next(){
    value,err:=cloneValue(iter.Value(),path);if err!=nil{return out,err}
    out.SetMapIndex(iter.Key(),value)
   }
  }
 case reflect.Slice:
  if !v.IsNil(){
   out.Set(reflect.MakeSlice(v.Type(),v.Len(),v.Len()))
   for i:=0;i<v.Len();i++ {
    child,err:=cloneValue(v.Index(i),path);if err!=nil{return out,err}
    out.Index(i).Set(child)
   }
  }
 case reflect.Array:
  for i:=0;i<v.Len();i++ {
   child,err:=cloneValue(v.Index(i),path);if err!=nil{return out,err}
   out.Index(i).Set(child)
  }
 case reflect.Struct:
  for i:=0;i<v.NumField();i++ {
   field:=v.Type().Field(i)
   if field.PkgPath!="" {return out,fmt.Errorf("%w: unsupported private field at %s",ErrInvalidDestination,path)}
   child,err:=cloneValue(v.Field(i),path);if err!=nil{return out,err}
   out.Field(i).Set(child)
  }
 case reflect.Func,reflect.Chan,reflect.UnsafePointer:
  return out,fmt.Errorf("%w: unsupported type at %s",ErrInvalidDestination,path)
 default:out.Set(v)
 }
 return out,nil
}

func decodeInto(dst reflect.Value,src any,path string) error {
 if src==nil {
  switch dst.Kind(){
  case reflect.Pointer,reflect.Map,reflect.Slice,reflect.Interface:
   dst.SetZero();return nil
  default:return fmt.Errorf("%w: null at %s",ErrConfigType,path)
  }
 }
 if dst.Kind()==reflect.Pointer {
  if dst.IsNil(){dst.Set(reflect.New(dst.Type().Elem()))}
  return decodeInto(dst.Elem(),src,path)
 }
 typeError:=func()error{return fmt.Errorf("%w: %s expects %s, got %T",ErrConfigType,path,dst.Type(),src)}
 switch dst.Kind() {
 case reflect.Struct:
  m,ok:=src.(map[string]any);if !ok{return typeError()}
  fields:=make(map[string]int)
  for i:=0;i<dst.NumField();i++ {
   sf:=dst.Type().Field(i)
   if sf.PkgPath!="" {continue}
   name:=sf.Tag.Get("conf")
   if name=="-" {continue}
   if name=="" {name=sf.Name}
   if _,exists:=fields[name];exists{return fmt.Errorf("%w: duplicate field %s",ErrInvalidDestination,name)}
   fields[name]=i
  }
  for name,value:=range m {
   i,ok:=fields[name]
   if !ok{return fmt.Errorf("%w: unknown field %s at %s",ErrConfigType,name,path)}
   if err:=decodeInto(dst.Field(i),value,joinPath(path,name));err!=nil{return err}
  }
  return nil
 case reflect.Map:
  m,ok:=src.(map[string]any);if !ok || dst.Type().Key().Kind()!=reflect.String{return typeError()}
  next:=reflect.MakeMapWithSize(dst.Type(),len(m))
  for key,value:=range m {
   elem:=reflect.New(dst.Type().Elem()).Elem()
   if err:=decodeInto(elem,value,joinPath(path,key));err!=nil{return err}
   next.SetMapIndex(reflect.ValueOf(key).Convert(dst.Type().Key()),elem)
  }
  dst.Set(next);return nil
 case reflect.Slice,reflect.Array:
  items,ok:=src.([]any);if !ok{return typeError()}
  if dst.Kind()==reflect.Array && dst.Len()!=len(items){return typeError()}
  if dst.Kind()==reflect.Slice {dst.Set(reflect.MakeSlice(dst.Type(),len(items),len(items)))}
  for i,value:=range items {
   if err:=decodeInto(dst.Index(i),value,fmt.Sprintf("%s[%d]",path,i));err!=nil{return err}
  }
  return nil
 case reflect.String:
  x,ok:=src.(string);if !ok{return typeError()};dst.SetString(x);return nil
 case reflect.Bool:
  x,ok:=src.(bool);if !ok{return typeError()};dst.SetBool(x);return nil
 case reflect.Int,reflect.Int8,reflect.Int16,reflect.Int32,reflect.Int64:
  x,ok:=src.(int64);if !ok || dst.OverflowInt(x){return typeError()};dst.SetInt(x);return nil
 case reflect.Uint,reflect.Uint8,reflect.Uint16,reflect.Uint32,reflect.Uint64:
  x,ok:=src.(int64);if !ok || x<0 || dst.OverflowUint(uint64(x)){return typeError()};dst.SetUint(uint64(x));return nil
 case reflect.Float32,reflect.Float64:
  var x float64
  switch value:=src.(type) {
  case int64:
   x=float64(value)
   if int64(x)!=value{return typeError()}
  case float64:x=value
  default:return typeError()
  }
  if math.IsNaN(x)||math.IsInf(x,0)||dst.OverflowFloat(x){return typeError()}
  if dst.Kind()==reflect.Float32 && _,ok:=src.(int64);ok && float64(float32(x))!=x{return typeError()}
  dst.SetFloat(x);return nil
 default:return fmt.Errorf("%w: unsupported destination type %s at %s",ErrInvalidDestination,dst.Type(),path)
 }
}
func joinPath(parent,child string) string {
 if parent=="" {return child}
 return parent+"."+child
}
