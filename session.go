package van

import (
	"net"

	"github.com/reusee/closer"
	ic "github.com/reusee/inf-chan"
)

type Session struct {
	closer.Closer
	*Signaler

	id    uint64
	conns []net.Conn

	newConnIn chan net.Conn
	newConn   chan net.Conn
}

func makeSession() *Session {
	session := &Session{
		Closer:    closer.NewCloser(),
		Signaler:  NewSignaler(),
		newConnIn: make(chan net.Conn),
		newConn:   make(chan net.Conn),
	}
	ic.Link(session.newConnIn, session.newConn)
	session.OnClose(func() {
		close(session.newConnIn)
	})
	go session.start()
	return session
}

func (s *Session) start() {
	for {
		select {
		case <-s.WaitClosing: // exit
			return
		case conn := <-s.newConn: // new connection
			s.addConn(conn)
		}
	}
}

func (s *Session) addConn(conn net.Conn) {
	s.conns = append(s.conns, conn)
	s.Signal("NewConn")
}
