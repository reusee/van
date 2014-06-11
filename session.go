package van

import (
	"net"

	"github.com/reusee/closer"
	ic "github.com/reusee/inf-chan"
)

type Session struct {
	closer.Closer
	*Signaler
	sigClose chan bool

	id    uint64
	conns []net.Conn

	newConnIn chan net.Conn
	newConn   chan net.Conn
}

func makeSession() *Session {
	session := &Session{
		Signaler:  NewSignaler(),
		sigClose:  make(chan bool),
		newConnIn: make(chan net.Conn),
		newConn:   make(chan net.Conn),
	}
	ic.Link(session.newConnIn, session.newConn)
	session.OnClose(func() {
		close(session.sigClose)
		close(session.newConnIn)
	})
	go session.start()
	return session
}

func (s *Session) start() {
	for {
		select {
		case <-s.sigClose: // exit
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
