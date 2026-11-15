package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

type M = map[string]any

func ID(prefix string) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}
func Now() float64 { return float64(time.Now().UnixMilli()) / 1000 }
func Hash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func JSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// JSONB uses text parameters so pgx Exec mode (Neon transaction pooling) never encodes JSON as bytea.
func JSONB(v any) string { return string(JSON(v)) }
func Obj(v any) M {
	if m, ok := v.(map[string]any); ok && m != nil {
		return m
	}
	b, _ := json.Marshal(v)
	m := M{}
	_ = json.Unmarshal(b, &m)
	if m == nil {
		m = M{}
	}
	return m
}
func Arr(v any) []any {
	if a, ok := v.([]any); ok && a != nil {
		return a
	}
	b, _ := json.Marshal(v)
	a := []any{}
	_ = json.Unmarshal(b, &a)
	if a == nil {
		a = []any{}
	}
	return a
}
func Maps(v any) []M {
	out := []M{}
	for _, x := range Arr(v) {
		out = append(out, Obj(x))
	}
	return out
}
func Str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
func Num(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	default:
		f, _ := strconv.ParseFloat(Str(v), 64)
		return f
	}
}
func Int(v any) int   { return int(Num(v)) }
func Bool(v any) bool { b, _ := v.(bool); return b }
func Strings(v any) []string {
	out := []string{}
	for _, x := range Arr(v) {
		out = append(out, Str(x))
	}
	return out
}
func Copy(m M) M {
	out := M{}
	_ = json.Unmarshal(JSON(m), &out)
	if out == nil {
		out = M{}
	}
	return out
}
func Merge(a, b M) M {
	c := Copy(a)
	for k, v := range b {
		c[k] = v
	}
	return c
}
func Default(m M, k string, v any) {
	if _, ok := m[k]; !ok {
		m[k] = v
	}
}
func Ptr(f float64) *float64 { return &f }
func Min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func Max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func Has(a []string, s string) bool {
	for _, x := range a {
		if x == s {
			return true
		}
	}
	return false
}
