package van

import "sync"

type Signaler struct {
	cbs  map[string][]interface{}
	lock sync.Mutex
}

func NewSignaler() *Signaler {
	return &Signaler{
		cbs: make(map[string][]interface{}),
	}
}

func (s *Signaler) OnSignal(signal string, f interface{}) {
	s.lock.Lock()
	s.cbs[signal] = append(s.cbs[signal], f)
	s.lock.Unlock()
}

func (s *Signaler) Signal(signal string, args ...interface{}) {
	for _, f := range s.cbs[signal] {
		switch fun := f.(type) {
		case func():
			fun()
		case func(interface{}):
			fun(args[0])
		case func(...interface{}):
			fun(args...)
		default:
			panic("invalid signal hadler")
		}
	}
}
