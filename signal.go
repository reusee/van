package van

type Signaler struct {
	cbs   map[string][]interface{}
	calls chan func()
}

func NewSignaler() *Signaler {
	s := &Signaler{
		cbs:   make(map[string][]interface{}),
		calls: make(chan func()),
	}
	go func() {
		for f := range s.calls {
			if f == nil {
				return
			}
			f()
		}
	}()
	return s
}

func (s *Signaler) CloseSignaler() {
	s.calls <- nil
}

func (s *Signaler) OnSignal(signal string, f interface{}) {
	s.calls <- func() {
		s.cbs[signal] = append(s.cbs[signal], f)
	}
}

func (s *Signaler) Signal(signal string, args ...interface{}) {
	s.calls <- func() {
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
}
