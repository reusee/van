package van

import (
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/reusee/closer"
	ic "github.com/reusee/inf-chan"
)

type Server struct {
	closer.Closer

	sessions     map[int64]*Session
	newTransport chan transportInfo

	newSessionIn chan *Session
	NewSession   chan *Session
}

type transportInfo struct {
	transport Transport
	sessionId int64
}

func NewServer(addrStr string) (*Server, error) {
	ln, err := net.Listen("tcp", addrStr)
	if err != nil {
		return nil, err
	}
	server := &Server{
		Closer:       closer.NewCloser(),
		sessions:     make(map[int64]*Session),
		newTransport: make(chan transportInfo),
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
			transport, err := ln.Accept()
			if err != nil {
				if server.IsClosing { // close normally
					return
				} else {
					log.Fatal(err)
				}
			}
			go server.handleClient(transport)
		}
	}()

	// transport / session manager
	go func() {
		for {
			select {
			case <-server.WaitClosing:
				return
			case info := <-server.newTransport:
				session, ok := server.sessions[info.sessionId]
				if ok { // existing session
					session.newTransport <- info.transport
				} else { // new session
					session := server.newSession(info.sessionId, info.transport)
					server.sessions[info.sessionId] = session
					server.newSessionIn <- session
				}
			}
		}
	}()

	return server, nil
}

func (s *Server) handleClient(transport Transport) {
	var sessionId int64
	var err error
	// read session id
	err = transport.SetReadDeadline(time.Now().Add(time.Second * 4))
	if err != nil {
		return
	}
	err = binary.Read(transport, binary.LittleEndian, &sessionId)
	if err != nil {
		return
	}
	transport.SetReadDeadline(time.Time{})
	// send to session manager
	select {
	case s.newTransport <- transportInfo{
		transport: transport,
		sessionId: sessionId,
	}:
	default:
	}
}

func (s *Server) newSession(sessionId int64, transport Transport) *Session {
	session := makeSession()
	session.id = sessionId
	session.newTransport <- transport
	return session
}

var sessionPathPattern = regexp.MustCompile("/[0-9]+")

func (s *Server) StartDebug(addr string) {
	http.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		path := req.URL.Path
		if path == "/" { // index
			w.Write([]byte(`
<html>
	<body>
		<p><a href="/debug/pprof">pprof</a></p>
		`))
			for id, _ := range s.sessions {
				w.Write([]byte(fmt.Sprintf(`
		<p><a href="/%d">%d</a></p>
			`, id, id)))
			}
			w.Write([]byte(`
	</body>
</html>
		`))
		} else if sessionPathPattern.MatchString(path) { // session
			sessionId, err := strconv.ParseInt(path[1:], 10, 64)
			if err == nil {
				if session, ok := s.sessions[sessionId]; ok {
					session.handleHttp(w)
				}
			}
		}
	})
	go http.ListenAndServe(addr, nil)
}
