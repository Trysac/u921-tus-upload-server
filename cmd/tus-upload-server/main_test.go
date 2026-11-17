package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunUsage(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	code := Run([]string{"-unknown-flag"}, stdout, stderr)
	if code != 2 {
		t.Errorf("expected exit code 2 on flag error, got %d", code)
	}
}

func TestRunInvalidDir(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	// Invalid dir path containing null byte fails os.MkdirAll on both Unix and Windows
	code := Run([]string{"-dir", "invalid\x00dir"}, stdout, stderr)
	if code != 1 {
		t.Errorf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "Error opening dir store") {
		t.Errorf("stderr = %q", stderr.String())
	}
}
