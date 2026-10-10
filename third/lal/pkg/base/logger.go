package base

import (
	"fmt"
	"github.com/q191201771/naza/pkg/nazalog"
	"sync"
)

// nazalog requires callers to serialize Init with all logging. Install one
// wrapper during package initialization; prefixed loggers share the same lock.
// This preserves log configuration changes across embedded media restarts.
type synchronizedLogger struct {
	logger nazalog.Logger
	mu     *sync.RWMutex
}

func init() {
	Log = &synchronizedLogger{logger: Log, mu: &sync.RWMutex{}}
	nazalog.SetGlobalLogger(Log)
}

var _ nazalog.Logger = (*synchronizedLogger)(nil)

func (l *synchronizedLogger) Trace(v ...interface{}) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	l.logger.Out(nazalog.LevelTrace, 2, fmt.Sprint(v...))
}

func (l *synchronizedLogger) Debug(v ...interface{}) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	l.logger.Out(nazalog.LevelDebug, 2, fmt.Sprint(v...))
}

func (l *synchronizedLogger) Info(v ...interface{}) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	l.logger.Out(nazalog.LevelInfo, 2, fmt.Sprint(v...))
}

func (l *synchronizedLogger) Warn(v ...interface{}) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	l.logger.Out(nazalog.LevelWarn, 2, fmt.Sprint(v...))
}

func (l *synchronizedLogger) Error(v ...interface{}) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	l.logger.Out(nazalog.LevelError, 2, fmt.Sprint(v...))
}

func (l *synchronizedLogger) Fatal(v ...interface{}) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	l.logger.Fatal(v...)
}

func (l *synchronizedLogger) Panic(v ...interface{}) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	l.logger.Panic(v...)
}

func (l *synchronizedLogger) Print(v ...interface{}) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	l.logger.Print(v...)
}

func (l *synchronizedLogger) Println(v ...interface{}) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	l.logger.Println(v...)
}

func (l *synchronizedLogger) Fatalln(v ...interface{}) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	l.logger.Fatalln(v...)
}

func (l *synchronizedLogger) Panicln(v ...interface{}) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	l.logger.Panicln(v...)
}

func (l *synchronizedLogger) Tracef(format string, v ...interface{}) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	l.logger.Out(nazalog.LevelTrace, 2, fmt.Sprintf(format, v...))
}

func (l *synchronizedLogger) Debugf(format string, v ...interface{}) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	l.logger.Out(nazalog.LevelDebug, 2, fmt.Sprintf(format, v...))
}

func (l *synchronizedLogger) Infof(format string, v ...interface{}) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	l.logger.Out(nazalog.LevelInfo, 2, fmt.Sprintf(format, v...))
}

func (l *synchronizedLogger) Warnf(format string, v ...interface{}) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	l.logger.Out(nazalog.LevelWarn, 2, fmt.Sprintf(format, v...))
}

func (l *synchronizedLogger) Errorf(format string, v ...interface{}) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	l.logger.Out(nazalog.LevelError, 2, fmt.Sprintf(format, v...))
}

func (l *synchronizedLogger) Fatalf(format string, v ...interface{}) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	l.logger.Fatalf(format, v...)
}

func (l *synchronizedLogger) Panicf(format string, v ...interface{}) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	l.logger.Panicf(format, v...)
}

func (l *synchronizedLogger) Printf(format string, v ...interface{}) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	l.logger.Printf(format, v...)
}

func (l *synchronizedLogger) Out(level nazalog.Level, depth int, text string) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	l.logger.Out(level, depth+1, text)
}

func (l *synchronizedLogger) Assert(expected, actual interface{}, info ...string) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	l.logger.Assert(expected, actual, info...)
}

func (l *synchronizedLogger) Sync() {
	l.mu.RLock()
	defer l.mu.RUnlock()
	l.logger.Sync()
}

func (l *synchronizedLogger) Output(depth int, text string) error {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.logger.Output(depth+1, text)
}

func (l *synchronizedLogger) GetOption() nazalog.Option {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.logger.GetOption()
}

func (l *synchronizedLogger) Init(options ...nazalog.ModOption) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.logger.Init(options...)
}

func (l *synchronizedLogger) WithPrefix(prefix string) nazalog.Logger {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return &synchronizedLogger{logger: l.logger.WithPrefix(prefix), mu: l.mu}
}
