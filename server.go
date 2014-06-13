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
		Closer:       closer.NewCloser(),
		sessions:     make(map[uint64]*Session),
		connSidChan:  make(chan connSidInfo),
		newSessionIn: make(chan *Session),
		NewSession:   make(chan *Session),
	}
	l1 := ic.Link(server.newSessionIn, server.NewSession)
	server.OnClose(func() {
		ln.Close() // close listener
		close(l1)
	})

	// accept
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				if server.IsClosing { // close normally
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
			case <-server.WaitClosing:
				return
			case info := <-server.connSidChan:
				session, ok := server.sessions[info.sessionId]
				if ok { // existing session
					session.newConn <- info.conn
				} else { // new session
					session := server.newSession(info.sessionId, info.conn)
					server.sessions[info.sessionId] = session
					server.newSessionIn <- session
				}
			}
		}
	}()

	return server, nil
}

func (s *Server) handleClient(conn net.Conn) {
	var sessionId uint64
	var err error
	// read session id
	err = conn.SetReadDeadline(time.Now().Add(time.Second * 4))
	if err != nil {
		return
	}
	err = binary.Read(conn, binary.LittleEndian, &sessionId)
	if err != nil {
		return
	}
	conn.SetReadDeadline(time.Time{})
	// send to session manager
	select {
	case s.connSidChan <- connSidInfo{
		conn:      conn,
		sessionId: sessionId,
	}:
	default:
	}
}

func (s *Server) newSession(sessionId uint64, conn net.Conn) *Session {
	session := makeSession()
	session.id = sessionId
	session.newConn <- conn
	return session
}
