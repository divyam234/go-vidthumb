package previewer

import "testing"

func TestSetLogLevel(t *testing.T) {
	original := CurrentLogLevel()
	t.Cleanup(func() { SetLogLevel(original) })

	SetLogLevel(LogLevelWarning)
	if got := CurrentLogLevel(); got != LogLevelWarning {
		t.Fatalf("CurrentLogLevel() = %d, want %d", got, LogLevelWarning)
	}

	SetLogLevel(LogLevelError)
	if got := CurrentLogLevel(); got != LogLevelError {
		t.Fatalf("CurrentLogLevel() = %d, want %d", got, LogLevelError)
	}
}
