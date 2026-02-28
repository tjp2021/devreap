package logger

import (
	"fmt"
	"os"
)

func (l *Logger) checkRotation() {
	if l.file == nil || l.maxSize <= 0 {
		return
	}

	info, err := l.file.Stat()
	if err != nil {
		return
	}

	if info.Size() < l.maxSize {
		return
	}

	l.rotate()
}

func (l *Logger) rotate() {
	l.file.Close()

	// shift existing rotated logs
	for i := l.maxFiles - 1; i > 0; i-- {
		old := fmt.Sprintf("%s.%d", l.logPath, i)
		new := fmt.Sprintf("%s.%d", l.logPath, i+1)
		os.Rename(old, new)
	}

	// rotate current log
	os.Rename(l.logPath, l.logPath+".1")

	// delete excess
	excess := fmt.Sprintf("%s.%d", l.logPath, l.maxFiles+1)
	os.Remove(excess)

	// open new file
	f, err := os.OpenFile(l.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	l.file = f
	l.writer = f
}
