package van

import "sync"

type Signaler struct {
	cbs  map[string][]func()
	lock sync.Mutex
}

func NewSignaler() *Signaler {
	return &Signaler{
		cbs: make(map[string][]func()),
	}
}

func (s *Signaler) OnSignal(signal string, f func()) {
	s.lock.Lock()
	s.cbs[signal] = append(s.cbs[signal], f)
	s.lock.Unlock()
}

func (s *Signaler) Signal(signal string) {
	for _, f := range s.cbs[signal] {
		f()
	}
}
