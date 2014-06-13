package van

import (
	"fmt"
	"math/rand"
	"testing"
)

func TestSend(t *testing.T) {
	addr := fmt.Sprintf("127.0.0.1:%d", rand.Intn(30000)+20000)

	server, err := NewServer(addr)
	if err != nil {
		t.Fatalf("NewServer %v", err)
	}
	defer server.Close()

	n := 100000
	serverDone := make(chan bool)

	go func() {
		for session := range server.NewSession {
			go func() {
				session.OnSignal("Log", func(arg interface{}) {
					fmt.Printf("SERVER: %s\n", arg.(string))
				})
				i := 0
				for data := range session.Recv {
					if string(data) != fmt.Sprintf("%d", i) {
						t.Fatalf("Recv")
					}
					fmt.Printf("from client %s\n", data)
					i++
					if i == n {
						close(serverDone)
					}
				}
			}()
		}
	}()

	client, err := NewClient(addr)
	if err != nil {
		t.Fatalf("NewClient %v", err)
	}
	client.OnSignal("Log", func(arg interface{}) {
		fmt.Printf("CLIENT: %s\n", arg.(string))
	})
	for i := 0; i < 16; i++ {
		err = client.NewConn()
		if err != nil {
			t.Fatalf("NewConn %v", err)
		}
	}

	for i := 0; i < n; i++ {
		client.Send([]byte(fmt.Sprintf("%d", i)))
	}

	<-serverDone

	fmt.Printf("resend %d\n", client.StatResend)
}
