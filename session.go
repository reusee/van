package van

import (
	"encoding/binary"
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
	errConn   chan net.Conn

	packetsIn chan []byte
	packets   chan []byte

	getConnsLen chan int
}

func makeSession() *Session {
	session := &Session{
		Closer:      closer.NewCloser(),
		Signaler:    NewSignaler(),
		newConnIn:   make(chan net.Conn),
		newConn:     make(chan net.Conn),
		packetsIn:   make(chan []byte),
		packets:     make(chan []byte),
		getConnsLen: make(chan int),
	}
	l1 := ic.Link(session.newConnIn, session.newConn)
	l2 := ic.Link(session.packetsIn, session.packets)
	session.OnClose(func() {
		close(l1)
		close(l2)
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
		case packet := <-s.packets: // incoming packet
			//TODO
			_ = packet

		// getters
		case s.getConnsLen <- len(s.conns):
		}
	}
}

func (s *Session) addConn(conn net.Conn) {
	s.conns = append(s.conns, conn)
	s.Signal("NewConn")
	// close conn when session close
	s.OnClose(func() {
		conn.Close()
	})
	// start reader
	go func() {
		var length uint16
		for {
			// read packet length
			err := binary.Read(conn, binary.LittleEndian, &length)
			if err != nil {
				if s.IsClosing { // session is closing
					return
				} else { // conn error
					s.errConn <- conn
					return
				}
			}
			// read packet
			packetData := make([]byte, length)
			n, err := conn.Read(packetData)
			if err != nil || n != int(length) {
				if s.IsClosing { // session is closing
					return
				} else { // conn error
					s.errConn <- conn
					return
				}
			}
			s.packetsIn <- packetData
		}
	}()
}
