package van

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

func TestInterrupt(t *testing.T) {
	addr := fmt.Sprintf("127.0.0.1:%d", rand.Intn(30000)+20000)

	server, err := NewServer(addr)
	if err != nil {
		t.Fatalf("NewServer %v", err)
	}
	defer server.Close()

	done := make(chan bool)
	go func() {
		for session := range server.NewSession {
			go func() {
				/*
					session.OnSignal("Log", func(arg interface{}) {
						fmt.Printf("SERVER: %s\n", arg.(string))
					})
				*/
				n := 0
				for data := range session.Recv {
					if n%1000 == 0 {
						fmt.Printf("=====================> %s\n", data)
					}
					n++
					if n > 30000 {
						close(done)
						return
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
	client.OnSignal("DelConn", func(arg interface{}) {
		nConns := arg.(int)
		fmt.Printf("current conns %d\n", nConns)
		err = client.NewConn()
		if err != nil {
			t.Fatalf("NewConn %v", err)
		}
	})
	for i := 0; i < 16; i++ {
		err = client.NewConn()
		if err != nil {
			t.Fatalf("NewConn %v", err)
		}
	}

	go func() {
		i := 0
		for {
			client.Send([]byte(fmt.Sprintf("%d", i)))
			i++
		}
	}()

	go func() {
		for _ = range time.NewTicker(time.Millisecond * 200).C {
			client.CloseRandomConn()
		}
	}()

	<-done
}
