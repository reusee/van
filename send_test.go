package van

import (
	"fmt"
	"math/rand"
	"strconv"
	"testing"
)

func TestSend(t *testing.T) {
	addr := fmt.Sprintf("127.0.0.1:%d", rand.Intn(30000)+20000)

	server, err := NewServer(addr)
	if err != nil {
		t.Fatalf("NewServer %v", err)
	}
	defer server.Close()

	n := 30000
	serverDone := make(chan bool)

	go func() {
		for session := range server.NewSession {
			go func() {
				/*
					session.OnSignal("Log", func(arg interface{}) {
						fmt.Printf("SERVER: %s\n", arg.(string))
					})
				*/
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
	/*
		client.OnSignal("Log", func(arg interface{}) {
			fmt.Printf("CLIENT: %s\n", arg.(string))
		})
	*/
	for i := 0; i < 16; i++ {
		err = client.NewTransport()
		if err != nil {
			t.Fatalf("NewTransport %v", err)
		}
	}

	for i := 0; i < n; i++ {
		serial := client.Send([]byte(fmt.Sprintf("%d", i)))
		client.OnSignal("Ack "+strconv.Itoa(int(serial)), func() bool {
			fmt.Printf("CLIENT: Ack %d\n", serial)
			return true
		})
	}

	<-serverDone

	fmt.Printf("resend %d\n", <-client.getStatResend)
}
