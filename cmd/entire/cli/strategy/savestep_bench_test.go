package strategy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/entireio/cli/cmd/entire/cli/benchutil"
	"github.com/entireio/cli/cmd/entire/cli/paths"
	"github.com/entireio/cli/cmd/entire/cli/session"
)

// BenchmarkSaveStep measures the per-prompt checkpoint creation path.
// This is the most frequently called operation — invoked every time an agent
// completes a tool use and the CLI saves a checkpoint to the shadow branch.
//
// Each iteration uses a fresh repo (b.StopTimer/b.StartTimer) to ensure
// clean state, since SaveStep creates commits on the shadow branch.
//
// Key operations measured:
//   - OpenRepository (go-git PlainOpen)
//   - loadSessionState / initializeSession (session state I/O)
//   - WriteTemporary (git tree building, blob creation, commit)
//   - saveSessionState (write session JSON)
//
// Dimensions: modified file count × prior checkpoints on shadow branch.
func BenchmarkSaveStep(b *testing.B) {
	b.Run("FirstCheckpoint/5Files", benchSaveStep(5, 0))
	b.Run("FirstCheckpoint/50Files", benchSaveStep(50, 0))
	b.Run("Subsequent/5Files_5Prior", benchSaveStep(5, 5))
	b.Run("Subsequent/50Files_5Prior", benchSaveStep(50, 5))
	b.Run("Subsequent/200Files_20Prior", benchSaveStep(200, 20))
}

func benchSaveStep(fileCount, priorCheckpoints int) func(*testing.B) {
	return func(b *testing.B) {
		b.ReportAllocs()

		for range b.N {
			b.StopTimer()

			br := benchutil.NewBenchRepo(b, benchutil.RepoOpts{FileCount: fileCount})
			sessionID := "bench-savestep-session"

			// Seed prior checkpoints on the shadow branch if needed
			if priorCheckpoints > 0 {
				br.SeedShadowBranch(b, sessionID, priorCheckpoints, min(5, fileCount))
				br.CreateSessionState(b, benchutil.SessionOpts{
					SessionID:    sessionID,
					Phase:        session.PhaseActive,
					StepCount:    priorCheckpoints,
					FilesTouched: benchFileNames(fileCount),
				})
			}

			// Write transcript file (required by SaveStep for metadata)
			transcript := benchutil.GenerateTranscript(benchutil.TranscriptOpts{
				MessageCount:    10,
				AvgMessageBytes: 200,
				IncludeToolUse:  true,
				FilesTouched:    benchFileNames(min(5, fileCount)),
			})
			br.WriteTranscriptFile(b, sessionID, transcript)

			// Modify files in the working tree to simulate agent edits
			modifiedFiles := make([]string, 0, min(5, fileCount))
			for i := range min(5, fileCount) {
				name := fmt.Sprintf("src/file_%03d.go", i)
				content := benchutil.GenerateGoFile(7000+i, 100)
				br.WriteFile(b, name, content)
				modifiedFiles = append(modifiedFiles, name)
			}

			metadataDir := paths.SessionMetadataDirFromSessionID(sessionID)
			metadataDirAbs := filepath.Join(br.Dir, metadataDir)
			if err := os.MkdirAll(metadataDirAbs, 0o750); err != nil {
				b.Fatalf("mkdir metadata: %v", err)
			}

			// Write a minimal transcript to the metadata dir on disk
			transcriptPath := filepath.Join(metadataDirAbs, paths.TranscriptFileName)
			if err := os.WriteFile(transcriptPath, transcript, 0o600); err != nil {
				b.Fatalf("write transcript: %v", err)
			}

			//nolint:usetesting // b.Chdir() restores only once at cleanup; we need a fresh dir each iteration
			if err := os.Chdir(br.Dir); err != nil {
				b.Fatalf("chdir: %v", err)
			}
			paths.ClearWorktreeRootCache()
			session.ClearGitCommonDirCache()

			step := StepContext{
				SessionID:      sessionID,
				ModifiedFiles:  modifiedFiles,
				NewFiles:       []string{},
				DeletedFiles:   []string{},
				MetadataDir:    metadataDir,
				MetadataDirAbs: metadataDirAbs,
				CommitMessage:  fmt.Sprintf("Checkpoint %d", priorCheckpoints+1),
				AuthorName:     "Bench",
				AuthorEmail:    "bench@test.com",
			}

			b.StartTimer()

			s := &ManualCommitStrategy{}
			if err := s.SaveStep(context.Background(), step); err != nil {
				b.Fatalf("SaveStep: %v", err)
			}
		}
	}
}

// benchFileNames generates file names matching the benchutil.NewBenchRepo layout.
func benchFileNames(count int) []string {
	names := make([]string, count)
	for i := range count {
		names[i] = fmt.Sprintf("src/file_%03d.go", i)
	}
	return names
}
