package domain

import (
	"reflect"
	"testing"
)

func TestJSONHelpersNeverReturnNilContainers(t *testing.T) {
	if Obj(nil) == nil || Arr(nil) == nil || Copy(nil) == nil {
		t.Fatal("nil container")
	}
	m := Obj(nil)
	Default(m, "x", 1)
	if Int(m["x"]) != 1 {
		t.Fatal(m)
	}
}
func TestCanonicalDigestAndCopy(t *testing.T) {
	a, b := M{"b": 2, "a": 1}, M{"a": 1, "b": 2}
	if Hash(a) != Hash(b) {
		t.Fatal("unstable canonical digest")
	}
	c := Copy(a)
	c["a"] = 3
	if reflect.DeepEqual(c, a) {
		t.Fatal("copy aliases original")
	}
	if len(ID("x")) < 20 {
		t.Fatal("weak identifier")
	}
}
