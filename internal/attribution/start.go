package attribution

import (
	"errors"
	"fmt"
	"time"
)

// StartOptions configures a watcher built for the daemon.
type StartOptions struct {
	StoreDir     string
	AdapterFile  string
	PollInterval time.Duration
	ScanInterval time.Duration

	// Windows is the merged per-class lifecycle_grace table.
	Windows map[string]time.Duration
	// Confirmations is the confirming scan count required on top of the window.
	Confirmations int

	Classifier Classifier
	Logf       func(format string, args ...any)
}

// Start opens the store, loads the harness descriptors, recovers the state a
// previous run left behind, and returns a watcher ready to run.
//
// Correctness never depends on the store surviving. An unreadable store, an
// unrecognized schema version, and a torn tail all resolve to starting with less
// history rather than to refusing to start, because every process then reads as
// unattributed and unattributed is the safe state.
func Start(opts StartOptions) (*Watcher, error) {
	registry, err := NewHarnessRegistry()
	if err != nil {
		return nil, fmt.Errorf("loading harness descriptors: %w", err)
	}
	if opts.AdapterFile != "" {
		registry.LoadUserFile(opts.AdapterFile)
	}

	store, err := OpenStore(StoreConfig{Dir: opts.StoreDir})
	if err != nil {
		return nil, fmt.Errorf("opening attribution store: %w", err)
	}

	clock := NewSystemClock()
	engine := NewEngine(EngineConfig{
		Windows:       opts.Windows,
		Confirmations: opts.Confirmations,
		ScanInterval:  opts.ScanInterval,
	}, clock)

	snapshot, _ := store.ReadSnapshot()
	var records []Record
	load, loadErr := store.Load()
	switch {
	case loadErr == nil:
		records = load.Records
	case errors.Is(loadErr, ErrUnknownSchemaVersion):
		// A store holding a version this binary does not know is ignored
		// entirely rather than guessed at.
		snapshot = nil
	default:
		if opts.Logf != nil {
			opts.Logf("attribution: reading the store failed, starting empty: %v", loadErr)
		}
		snapshot = nil
	}
	engine.Restore(Recover(snapshot, records))

	watcher := NewWatcher(WatcherConfig{
		PollInterval: opts.PollInterval,
		ScanInterval: opts.ScanInterval,
		Classifier:   opts.Classifier,
		Logf:         opts.Logf,
	}, store, registry, engine, clock)

	for _, finding := range registry.Findings() {
		watcher.addFinding(finding.Kind, finding.Detail)
	}
	return watcher, nil
}

// Close stops the watcher and releases the store. A clean stop writes a final
// snapshot, so a restart replays only the journal tail.
func (w *Watcher) Close() error {
	w.Stop()
	if w.store == nil {
		return nil
	}
	return w.store.Close()
}
