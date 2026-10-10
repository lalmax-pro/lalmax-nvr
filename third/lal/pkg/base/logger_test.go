package base

import (
	"sync"
	"testing"

	"github.com/q191201771/naza/pkg/nazalog"
)

func TestLogInitializationSerializesWithPrefixedWriters(t *testing.T) {
	underlying, err := nazalog.New(func(o *nazalog.Option) { o.IsToStdout = false; o.Level = nazalog.LevelLogNothing })
	if err != nil {
		t.Fatal(err)
	}
	log := &synchronizedLogger{logger: underlying, mu: &sync.RWMutex{}}
	prefixed := log.WithPrefix("media")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			prefixed.Debugf("frame %d", i)
			_ = prefixed.GetOption()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			if err := log.Init(func(o *nazalog.Option) { o.IsToStdout = false; o.Level = nazalog.LevelLogNothing }); err != nil {
				t.Error(err)
			}
		}
	}()
	wg.Wait()
	if err := log.Init(func(o *nazalog.Option) { o.IsToStdout = false; o.Level = nazalog.LevelInfo }); err != nil {
		t.Fatal(err)
	}
	if prefixed.GetOption().Level != nazalog.LevelInfo {
		t.Fatal("prefixed logger lost configuration update")
	}
}
