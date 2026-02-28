package notify

// Notifier sends user-visible notifications when processes are killed.
type Notifier interface {
	Notify(title, message string) error
}

// Noop is a no-op notifier for when notifications are disabled.
type Noop struct{}

func (n *Noop) Notify(_, _ string) error { return nil }
