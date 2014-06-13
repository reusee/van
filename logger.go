package van

import (
	"fmt"

	"github.com/reusee/closer"
	ic "github.com/reusee/inf-chan"
)

type Logger struct {
	closer.Closer
	logsIn chan string
	Logs   chan string
}

func newLogger() *Logger {
	logger := &Logger{
		Closer: closer.NewCloser(),
		logsIn: make(chan string),
		Logs:   make(chan string),
	}
	link := ic.Link(logger.logsIn, logger.Logs)
	logger.OnClose(func() {
		close(link)
	})
	return logger
}

func (l *Logger) Log(format string, args ...interface{}) {
	l.logsIn <- fmt.Sprintf(format, args...)
}
