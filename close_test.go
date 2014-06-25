package van

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

func TestClose(t *testing.T) {
	addr := fmt.Sprintf("127.0.0.1:%d", rand.Intn(30000)+20000)

	server, err := NewServer(addr)
	if err != nil {
		t.Fatalf("NewServer %v", err)
	}
	defer server.Close()

	go func() {
		for session := range server.NewSession {
			session.OnSignal("Log", func(arg interface{}) {
				fmt.Printf("SERVER: %s\n", arg.(string))
			})
			go func() {
				for packet := range session.Recv {
					if packet.Type == FIN {
						session.Finish(packet.Conn)
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

	for i := 0; i < 512; i++ {
		conn := client.NewConn()
		client.Finish(conn)
	}

	time.Sleep(time.Second * 1)
	fmt.Printf("%d\n", len(client.conns))
	if len(client.conns) != 0 {
		t.Fail()
	}
}
