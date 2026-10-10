package executor

import (
	"sync/atomic"
)

type countingWebsocketLifecycle struct {
	binds atomic.Int32
}

func (l *countingWebsocketLifecycle) Bind(func() error) error {
	l.binds.Add(1)
	return nil
}

func (*countingWebsocketLifecycle) End(string) {}
