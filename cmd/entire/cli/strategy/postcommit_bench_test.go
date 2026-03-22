package strategy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/entireio/cli/cmd/entire/cli/checkpoint/id"
	"github.com/entireio/cli/cmd/entire/cli/paths"
	"github.com/entireio/cli/cmd/entire/cli/session"
	"github.com/entireio/cli/cmd/entire/cli/trailers"
)

// BenchmarkPostCommit measures the full PostCommit hook execution time.
// This is the baseline before introducing a postCommitCache.
//
// Setup: 1 active session with a shadow branch checkpoint, then a commit
// with the Entire-Checkpoint trailer. PostCommit reads HEAD, finds the session,
// runs condensation (filesOverlapWithContent, CondenseSession, carry-forward).
func BenchmarkPostCommit(b *testing.B) {
	b.Run("SingleSession_Active", benchPostCommitSingleSession(session.PhaseActive))
	b.Run("SingleSession_Idle", benchPostCommitSingleSession(session.PhaseIdle))
	b.Run("MultipleSessions_2", benchPostCommitMultipleSessions(2))
	b.Run("MultipleSessions_3", benchPostCommitMultipleSessions(3))
}

func benchPostCommitSingleSession(phase session.Phase) func(*testing.B) {
	return func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			b.StopTimer()
			dir := benchSetupPostCommitRepo(b, phase, 1)
			b.Chdir(dir)
			paths.ClearWorktreeRootCache()
			b.StartTimer()

			s := &ManualCommitStrategy{}
			if err := s.PostCommit(context.Background()); err != nil {
				b.Fatalf("PostCommit: %v", err)
			}
		}
	}
}

func benchPostCommitMultipleSessions(sessionCount int) func(*testing.B) {
	return func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			b.StopTimer()
			dir := benchSetupPostCommitRepo(b, session.PhaseActive, sessionCount)
			b.Chdir(dir)
			paths.ClearWorktreeRootCache()
			b.StartTimer()

			s := &ManualCommitStrategy{}
			if err := s.PostCommit(context.Background()); err != nil {
				b.Fatalf("PostCommit: %v", err)
			}
		}
	}
}

// benchSetupPostCommitRepo creates a git repo with N sessions that have shadow branch
// checkpoints, then creates a commit with the Entire-Checkpoint trailer.
// Uses git CLI for add/commit operations to minimize pprof noise from setup code.
// Returns the repo directory path, ready for PostCommit() to run.
func benchSetupPostCommitRepo(b *testing.B, phase session.Phase, sessionCount int) string {
	b.Helper()

	dir := b.TempDir()
	// Resolve symlinks (macOS /var -> /private/var)
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}

	// Init repo and configure git user via CLI
	benchGitCmd(b, dir, "init")
	benchGitCmd(b, dir, "config", "user.name", "Bench User")
	benchGitCmd(b, dir, "config", "user.email", "bench@example.com")
	benchGitCmd(b, dir, "config", "commit.gpgsign", "false")

	// Create multiple files to make file overlap checks realistic
	for i := range 5 {
		name := fmt.Sprintf("src/file_%d.go", i)
		abs := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			b.Fatalf("mkdir: %v", err)
		}
		content := fmt.Sprintf("package main\nfunc f%d() {}\n", i)
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			b.Fatalf("write: %v", err)
		}
	}

	// Stage and commit via git CLI (much faster than go-git wt.Add per file)
	benchGitCmd(b, dir, "add", "-A")
	benchGitCmd(b, dir, "commit", "-m", "initial commit", "--no-gpg-sign")

	s := &ManualCommitStrategy{}

	// Chdir to repo dir for the entire setup (SaveStep, loadSessionState, etc.
	// all depend on paths.WorktreeRoot() which uses cwd). b.Chdir restores
	// the original directory when the benchmark function returns.
	b.Chdir(dir)
	paths.ClearWorktreeRootCache()

	// Set up each session with a shadow branch checkpoint
	modifiedFiles := []string{"src/file_0.go", "src/file_1.go"}
	for i := range sessionCount {
		sessionID := fmt.Sprintf("bench-session-%d", i)

		// Modify files with agent content
		for _, f := range modifiedFiles {
			abs := filepath.Join(dir, f)
			content := fmt.Sprintf("package main\n// modified by agent session %d\nfunc f() {}\n", i)
			if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
				b.Fatalf("write: %v", err)
			}
		}

		// Create metadata directory with transcript
		metadataDir := ".entire/metadata/" + sessionID
		metadataDirAbs := filepath.Join(dir, metadataDir)
		if err := os.MkdirAll(metadataDirAbs, 0o755); err != nil {
			b.Fatalf("mkdir: %v", err)
		}
		transcript := `{"type":"human","message":{"content":"implement feature"}}
{"type":"assistant","message":{"content":"I'll implement that for you."}}
`
		if err := os.WriteFile(filepath.Join(metadataDirAbs, paths.TranscriptFileName), []byte(transcript), 0o644); err != nil {
			b.Fatalf("write transcript: %v", err)
		}

		paths.ClearWorktreeRootCache()

		if err := s.SaveStep(context.Background(), StepContext{
			SessionID:      sessionID,
			ModifiedFiles:  modifiedFiles,
			NewFiles:       []string{},
			DeletedFiles:   []string{},
			MetadataDir:    metadataDir,
			MetadataDirAbs: metadataDirAbs,
			CommitMessage:  "Checkpoint 1",
			AuthorName:     "Bench",
			AuthorEmail:    "bench@test.com",
		}); err != nil {
			b.Fatalf("SaveStep: %v", err)
		}

		// Set the session phase
		state, err := s.loadSessionState(context.Background(), sessionID)
		if err != nil {
			b.Fatalf("load state: %v", err)
		}
		state.Phase = phase
		state.FilesTouched = modifiedFiles
		if err := s.saveSessionState(context.Background(), state); err != nil {
			b.Fatalf("save state: %v", err)
		}
	}

	// Create the user commit with checkpoint trailer (the commit PostCommit will process)
	cpID, err := id.Generate()
	if err != nil {
		b.Fatalf("generate ID: %v", err)
	}

	// Modify a file and commit with trailer via git CLI
	testFile := filepath.Join(dir, "src/file_0.go")
	if err := os.WriteFile(testFile, []byte("package main\n// modified by agent session 0\nfunc f() {}\n"), 0o644); err != nil {
		b.Fatalf("write: %v", err)
	}

	benchGitCmd(b, dir, "add", "src/file_0.go")
	commitMsg := fmt.Sprintf("implement feature\n\n%s: %s\n", trailers.CheckpointTrailerKey, cpID)
	benchGitCmd(b, dir, "commit", "-m", commitMsg, "--no-gpg-sign")

	return dir
}

// benchGitCmd runs a git command in the given directory for benchmark setup.
func benchGitCmd(b *testing.B, dir string, args ...string) {
	b.Helper()

	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		b.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}
