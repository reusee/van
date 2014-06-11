package van

import (
	"encoding/binary"
	"log"
	"net"
	"time"

	"github.com/reusee/closer"
	ic "github.com/reusee/inf-chan"
)

type Server struct {
	closer.Closer
	closing  bool
	sigClose chan bool

	sessions    map[uint64]*Session
	connSidChan chan connSidInfo

	newSessionIn chan *Session
	NewSession   chan *Session
}

type connSidInfo struct {
	conn      net.Conn
	sessionId uint64
}

func NewServer(addrStr string) (*Server, error) {
	ln, err := net.Listen("tcp", addrStr)
	if err != nil {
		return nil, err
	}
	server := &Server{
		sigClose:     make(chan bool),
		sessions:     make(map[uint64]*Session),
		connSidChan:  make(chan connSidInfo),
		newSessionIn: make(chan *Session),
		NewSession:   make(chan *Session),
	}
	ic.Link(server.newSessionIn, server.NewSession)

	// accept
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				if server.closing { // close normally
					return
				} else {
					log.Fatal(err)
				}
			}
			go server.handleClient(conn)
		}
	}()

	// conn / session manager
	go func() {
		for {
			select {
			case <-server.sigClose:
				return
			case info := <-server.connSidChan:
				session, ok := server.sessions[info.sessionId]
				if ok { // existing session
					session.newConnIn <- info.conn
				} else { // new session
					session := server.newSession(info.sessionId, info.conn)
					server.sessions[info.sessionId] = session
					server.newSessionIn <- session
				}
			}
		}
	}()

	// closer
	server.OnClose(func() {
		server.closing = true
		ln.Close() // close listener
		close(server.sigClose)
		close(server.newSessionIn)
	})

	return server, nil
}

func (s *Server) handleClient(conn net.Conn) {
	var sessionId uint64
	// read session id
	err := conn.SetReadDeadline(time.Now().Add(time.Second * 4))
	if err != nil {
		return
	}
	err = binary.Read(conn, binary.LittleEndian, &sessionId)
	if err != nil {
		return
	}
	// send to session manager
	s.connSidChan <- connSidInfo{
		conn:      conn,
		sessionId: sessionId,
	}
}

func (s *Server) newSession(sessionId uint64, conn net.Conn) *Session {
	session := makeSession()
	session.id = sessionId
	session.newConnIn <- conn
	return session
}
