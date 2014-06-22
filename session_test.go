package van

import (
	"testing"
	"time"
)

func TestNewSession(t *testing.T) {
	addr := "127.0.0.1:34500"

	serverDone := make(chan bool)
	var serverSession *Session
	newTransport := make(chan bool)

	// server
	server, err := NewServer(addr)
	if err != nil {
		t.Fatalf("NewServer")
	}
	defer server.Close()
	// listen session
	go func() {
		for session := range server.NewSession {
			go func() {
				serverSession = session
				session.OnSignal("NewTransport", func() {
					newTransport <- true
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
		t.Fatalf("client session fail")
	}

	// new transport
	for i := 0; i < 8; i++ {
		err = client.NewTransport()
		if err != nil {
			t.Fatalf("NewTransport: %v", err)
		}
		select {
		case <-newTransport:
			if <-client.getTransportCount != <-serverSession.getTransportCount {
				t.Fatalf("NewTransport")
			}
		case <-time.After(time.Second):
			t.Fatalf("NewTransport timeout")
		}
	}

}
