package scanner

import (
	"context"
	"testing"
	"time"
)

func TestBuildPortMapHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	started := time.Now()
	ports := buildPortMap(ctx)

	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("buildPortMap ignored cancellation and took %s", elapsed)
	}
	if len(ports) != 0 {
		t.Fatalf("expected no ports after cancellation, got %d processes", len(ports))
	}
}
