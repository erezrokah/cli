package strategy

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/entireio/cli/cmd/entire/cli/benchutil"
	"github.com/entireio/cli/cmd/entire/cli/paths"
	"github.com/entireio/cli/cmd/entire/cli/session"
)

// BenchmarkGetRewindPoints measures the cost of listing available rewind points.
// This is called when the user runs `entire rewind` to browse checkpoints.
//
// GetRewindPoints is read-only, so setup is done once per sub-benchmark.
//
// Key operations measured:
//   - OpenRepository (go-git PlainOpen)
//   - findSessionsForCommit (list + filter session states)
//   - ListTemporaryCheckpoints (shadow branch traversal per session)
//   - readSessionPrompt (commit → tree → prompt.txt per session)
//   - GetLogsOnlyRewindPoints (commit history scan + metadata branch lookup)
//
// Dimensions: checkpoints per session × session count.
func BenchmarkGetRewindPoints(b *testing.B) {
	b.Run("5Checkpoints_1Session", benchGetRewindPoints(5, 1))
	b.Run("20Checkpoints_1Session", benchGetRewindPoints(20, 1))
	b.Run("50Checkpoints_1Session", benchGetRewindPoints(50, 1))
	b.Run("20Checkpoints_3Sessions", benchGetRewindPoints(20, 3))
	b.Run("50Checkpoints_3Sessions", benchGetRewindPoints(50, 3))
}

func benchGetRewindPoints(checkpointsPerSession, sessionCount int) func(*testing.B) {
	return func(b *testing.B) {
		b.ReportAllocs()

		br := benchutil.NewBenchRepo(b, benchutil.RepoOpts{FileCount: 20})

		for i := range sessionCount {
			sessionID := fmt.Sprintf("bench-rewind-session-%d", i)

			// Seed shadow branch with checkpoints
			br.SeedShadowBranch(b, sessionID, checkpointsPerSession, min(5, 20))

			// Write transcript to metadata dir on disk
			transcript := benchutil.GenerateTranscript(benchutil.TranscriptOpts{
				MessageCount:    10,
				AvgMessageBytes: 200,
			})
			metadataDir := paths.SessionMetadataDirFromSessionID(sessionID)
			metadataDirAbs := fmt.Sprintf("%s/%s", br.Dir, metadataDir)
			if err := os.MkdirAll(metadataDirAbs, 0o750); err != nil {
				b.Fatalf("mkdir metadata: %v", err)
			}
			if err := os.WriteFile(metadataDirAbs+"/"+paths.TranscriptFileName, transcript, 0o600); err != nil {
				b.Fatalf("write transcript: %v", err)
			}

			// Create session state matching the shadow branch
			br.CreateSessionState(b, benchutil.SessionOpts{
				SessionID:    sessionID,
				Phase:        session.PhaseIdle,
				StepCount:    checkpointsPerSession,
				FilesTouched: rewindBenchFileNames(5),
			})
		}

		b.Chdir(br.Dir)
		paths.ClearWorktreeRootCache()
		session.ClearGitCommonDirCache()

		b.ResetTimer()
		for range b.N {
			paths.ClearWorktreeRootCache()
			session.ClearGitCommonDirCache()

			s := &ManualCommitStrategy{}
			points, err := s.GetRewindPoints(context.Background(), 50)
			if err != nil {
				b.Fatalf("GetRewindPoints: %v", err)
			}
			if len(points) == 0 {
				b.Fatal("GetRewindPoints returned 0 points")
			}
		}
	}
}

// rewindBenchFileNames generates file names for rewind benchmark sessions.
func rewindBenchFileNames(count int) []string {
	names := make([]string, count)
	for i := range count {
		names[i] = fmt.Sprintf("src/file_%03d.go", i)
	}
	return names
}
