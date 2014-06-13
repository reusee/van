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

	n := 50000
	serverDone := make(chan bool)

	go func() {
		for session := range server.NewSession {
			go func() {
				go func() {
					for log := range session.Logs {
						fmt.Printf("SERVER: %s\n", log)
					}
				}()
				i := 0
				for data := range session.Recv {
					if string(data) != fmt.Sprintf("%d", i) {
						t.Fatalf("Recv")
					}
					fmt.Printf("=============================> %s\n", data)
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
	go func() {
		for log := range client.Logs {
			fmt.Printf("CLIENT: %s\n", log)
		}
	}()
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
