package van

import (
	"testing"
	"time"
)

func TestConnect(t *testing.T) {
	addr := "127.0.0.1:34500"

	serverDone := make(chan bool)
	var serverSession *Session
	newConn := make(chan bool)

	// server
	server, err := NewServer(addr)
	if err != nil {
		t.Fatalf("NewServer")
	}
	defer server.Close()
	// listen conn
	go func() {
		for session := range server.NewSession {
			go func() {
				serverSession = session
				session.OnSignal("NewConn", func() {
					newConn <- true
				})
				close(serverDone)
			}()
		}
	}()

	// client
	client, err := NewClient(addr)
	if err != nil {
		t.Fatalf("NewClient")
	}
	defer client.Close()

	// test session id
	select {
	case <-serverDone:
		if serverSession == nil || serverSession.id != client.id {
			t.Fatalf("session fail")
		}
	case <-time.After(time.Second * 3):
		t.Fatalf("connect fail")
	}

	// new conn
	for i := 0; i < 8; i++ {
		err = client.NewConn()
		if err != nil {
			t.Fatalf("NewConn: %v", err)
		}
		select {
		case <-newConn:
			if len(client.conns) != len(serverSession.conns) {
				t.Fatalf("NewConn")
			}
		case <-time.After(time.Second):
			t.Fatalf("NewConn timeout")
		}
	}

}
