package van

import (
	"math/rand"
	"time"

	"net"
	_ "net/http/pprof"
)

func init() {
	rand.Seed(time.Now().UnixNano())

	//go http.ListenAndServe("0.0.0.0:60000", nil)
}

type Transport net.Conn
