package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/Gentleman-Programming/engram/internal/redact"
)

// runRedact implements `engram redact`. With --catalog-sha256 it prints the
// hash of the embedded credential catalog (so a release can check that its
// copy matches). Without flags it filters stdin to stdout with the same
// redactor the server applies to everything it stores.
func runRedact(args []string, in io.Reader, out io.Writer) error {
	switch {
	case len(args) == 0:
		raw, err := io.ReadAll(in)
		if err != nil {
			return err
		}
		_, err = io.WriteString(out, redact.Redact(string(raw)))
		return err
	case len(args) == 1 && args[0] == "--catalog-sha256":
		_, err := fmt.Fprintln(out, redact.CatalogSHA256())
		return err
	default:
		return errors.New("usage: engram redact [--catalog-sha256]  (filters stdin to stdout)")
	}
}
