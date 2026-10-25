#!/usr/bin/env bash
# Reads `go test` output on stdin and keeps only what is needed to act on a
# failure: failing tests with their detail, panics, compile errors, and the
# per-package FAIL lines. Passing packages collapse to one summary line.
awk '
  /^ok[ \t]/            { ok++; next }
  /no test files/       { next }
  /^(=== RUN|=== PAUSE|=== CONT|--- PASS|PASS$)/ { next }
  /^--- FAIL|^FAIL|^panic:|^\[build failed\]|^# |^\s+--- FAIL/ { keep = 1; print; next }
  /^[[:space:]]/ && keep { print; next }
  /\.go:[0-9]+(:[0-9]+)?:/ { print; next }
  /^(goroutine |exit status)/ { print; next }
  { keep = 0 }
  END { if (ok) printf "ok: %d package(s) passed\n", ok }
'
