// Command openapigen converts api/openapi.yaml to api/openapi.json, keeping
// the key order of the YAML. With -check it only reports whether the JSON is
// current (CI). Run it through "go generate ./api".
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"go.vicoworks.com/agent-reliability-lab/api/internal/openapi"
)

func main() {
	check := flag.Bool("check", false, "fail when openapi.json is out of date instead of writing it")
	dir := flag.String("dir", ".", "directory holding openapi.yaml")
	flag.Parse()
	src, err := os.ReadFile(filepath.Join(*dir, "openapi.yaml"))
	if err != nil {
		fail(err)
	}
	out, err := openapi.JSON(src)
	if err != nil {
		fail(err)
	}
	target := filepath.Join(*dir, "openapi.json")
	if *check {
		current, _ := os.ReadFile(target)
		if !bytes.Equal(current, out) {
			fail(fmt.Errorf("%s is out of date; run: go generate ./api", target))
		}
		return
	}
	if err = os.WriteFile(target, out, 0o644); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "openapigen:", err)
	os.Exit(1)
}
