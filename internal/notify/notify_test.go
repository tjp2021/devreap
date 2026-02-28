package notify

import "testing"

func TestEscapeAppleScript(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty string", "", ""},
		{"no special chars", "hello world", "hello world"},
		{"double quote", `say "hello"`, `say \"hello\"`},
		{"backslash", `path\to\file`, `path\\to\\file`},
		{"both", `"C:\Users\"`, `\"C:\\Users\\\"`},
		{"newline passthrough", "line1\nline2", "line1\nline2"},
		{"single quotes passthrough", "it's fine", "it's fine"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := escapeAppleScript(tt.input)
			if got != tt.want {
				t.Errorf("escapeAppleScript(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNoop_Notify(t *testing.T) {
	n := &Noop{}
	if err := n.Notify("title", "message"); err != nil {
		t.Errorf("Noop.Notify should return nil, got: %v", err)
	}
}

func TestMacOS_ImplementsNotifier(t *testing.T) {
	// Compile-time check that MacOS implements Notifier
	var _ Notifier = &MacOS{}
	var _ Notifier = &Noop{}
}
