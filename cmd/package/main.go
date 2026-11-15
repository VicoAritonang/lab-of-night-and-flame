// Build and verify deployment artifacts using only Go, including Unix ZIP permissions on Windows.
package main

import (
	"archive/zip"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func build(arch, out, target string, lambda bool) error {
	args := []string{"build", "-trimpath", "-ldflags=-s -w", "-o", out}
	if lambda {
		args = append(args, "-tags=lambda.norpc")
	}
	args = append(args, target)
	cmd := exec.Command("go", args...)
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
func run() error {
	if err := os.MkdirAll("dist", 0755); err != nil {
		return err
	}
	for _, b := range []struct {
		arch, out, target string
		lambda            bool
	}{{"arm64", "dist/bootstrap", "./cmd/lambda", true}, {"amd64", "dist/arl-sync-linux-amd64", "./", false}, {"amd64", "dist/arl-worker-linux-amd64", "./cmd/worker", false}} {
		if err := build(b.arch, b.out, b.target, b.lambda); err != nil {
			return err
		}
	}
	binary, err := elf.Open("dist/bootstrap")
	if err != nil {
		return err
	}
	if binary.Machine != elf.EM_AARCH64 || binary.Class != elf.ELFCLASS64 {
		binary.Close()
		return fmt.Errorf("bootstrap is not ELF64 ARM64")
	}
	for _, p := range binary.Progs {
		if p.Type == elf.PT_INTERP {
			binary.Close()
			return fmt.Errorf("bootstrap unexpectedly requires a dynamic interpreter")
		}
	}
	binary.Close()
	out, err := os.Create("dist/async-lambda-arm64.zip")
	if err != nil {
		return err
	}
	zw := zip.NewWriter(out)
	head := &zip.FileHeader{Name: "bootstrap", Method: zip.Deflate}
	head.SetMode(0755)
	head.SetModTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	entry, err := zw.CreateHeader(head)
	if err != nil {
		return err
	}
	in, err := os.Open("dist/bootstrap")
	if err != nil {
		return err
	}
	_, err = io.Copy(entry, in)
	in.Close()
	if err != nil {
		return err
	}
	if err = zw.Close(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	zr, err := zip.OpenReader("dist/async-lambda-arm64.zip")
	if err != nil {
		return err
	}
	if len(zr.File) != 1 || zr.File[0].Name != "bootstrap" || zr.File[0].Mode().Perm() != 0755 {
		zr.Close()
		return fmt.Errorf("invalid Lambda ZIP structure or permissions")
	}
	zr.Close()
	manifest := map[string]any{"runtime": "provided.al2023", "handler": "bootstrap", "lambda_architecture": "arm64", "sync_architecture": "amd64", "cgo": false, "files": map[string]any{}}
	for _, name := range []string{"bootstrap", "async-lambda-arm64.zip", "arl-sync-linux-amd64", "arl-worker-linux-amd64"} {
		file, err := os.Open(filepath.Join("dist", name))
		if err != nil {
			return err
		}
		hash := sha256.New()
		n, err := io.Copy(hash, file)
		file.Close()
		if err != nil {
			return err
		}
		manifest["files"].(map[string]any)[name] = map[string]any{"sha256": hex.EncodeToString(hash.Sum(nil)), "bytes": n}
	}
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile("dist/manifest.json", append(body, '\n'), 0644); err != nil {
		return err
	}
	fmt.Println("Built verified ARM64 bootstrap ZIP and AMD64 SYNC/local-worker binaries in dist/")
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
