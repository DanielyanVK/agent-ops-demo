---
name: go-test-fixer
description: Use when Go tests fail and you have to find and fix the bugs. Gives the order of work that avoids blocked commands.
---

# Fixing failing Go tests

1. Run `go test ./...` once, alone, with no other command in the same call.
2. Read each source file and its test with the Read tool, one file per call. Do not use shell loops, `cat`, `sed`, heredocs or scripts.
3. Fix each bug with the Edit tool. Never edit test files.
4. Run `go test ./...` once more, alone.
