package scanner

import (
	"testing"
	"time"

	"github.com/tjp2021/devreap/internal/config"
	"github.com/tjp2021/devreap/internal/patterns"
)

func BenchmarkScore(b *testing.B) {
	scorer := NewScorer(config.DefaultWeights())
	scorer.ResetCache([]ProcessInfo{
		{Name: "Finder", Cmdline: "/System/Library/CoreServices/Finder.app/Contents/MacOS/Finder"},
	})

	proc := ProcessInfo{
		PID:        1234,
		PPID:       1,
		Name:       "node",
		CreateTime: time.Now().Add(-5 * time.Hour),
		HasTTY:     false,
		Ports:      []uint32{3000},
	}
	pat := patterns.Pattern{Name: "mcp-server", MaxDuration: 4 * time.Hour}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		scorer.Score(proc, pat)
	}
}

func BenchmarkPatternMatch(b *testing.B) {
	registry, err := patterns.NewRegistry()
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		registry.Match("node", "mcp-server-test --port 3000")
	}
}

func BenchmarkFindOrphans(b *testing.B) {
	registry, err := patterns.NewRegistry()
	if err != nil {
		b.Fatal(err)
	}

	scorer := NewScorer(config.DefaultWeights())

	procs := make([]ProcessInfo, 500)
	for i := range procs {
		procs[i] = ProcessInfo{
			PID:        int32(1000 + i),
			PPID:       1,
			Name:       "node",
			Args:       "mcp-server-test",
			Cmdline:    "node mcp-server-test",
			CreateTime: time.Now().Add(-5 * time.Hour),
			HasTTY:     false,
		}
	}

	scorer.ResetCache(procs)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		FindOrphans(procs, registry, scorer, 0.6, nil)
	}
}
