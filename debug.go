package van

import (
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"regexp"
	"sort"
)

func (s *Session) setDebugEntries() {
	// connections
	s.addDebugEntry(func() (ret []string) {
		ret = append(ret, fmt.Sprintf("<Connections> %d", len(s.conns)))
		conns := make([]*Conn, 0, len(s.conns))
		for _, conn := range s.conns {
			conns = append(conns, conn)
		}
		sort.Sort(sortByStart(conns))
		for _, conn := range conns {
			ret = append(ret, fmt.Sprintf("%02d:%02d:%02d id %d serial %d ackSerial %d",
				conn.start.Hour(), conn.start.Minute(), conn.start.Second(),
				conn.Id, conn.serial, conn.ackSerial))
		}
		return
	})
	s.addDebugEntry(func() (ret []string) {
		ret = append(ret, fmt.Sprintf("<Closed Connection Ids> %d", s.closedConnIdEdge))
		ids := ""
		for id, _ := range s.closedConnIdMap {
			ids += fmt.Sprintf("%d ", id)
		}
		if len(ids) > 0 {
			ret = append(ret, ids)
		}
		return
	})
	// incoming packets
	s.addDebugEntry(func() (ret []string) {
		ret = append(ret, fmt.Sprintf("<Incoming Packets> %d", len(s.incomingPacketsMap)))
		return
	})
	// sending packets
	s.addDebugEntry(func() (ret []string) {
		ret = append(ret, fmt.Sprintf("<Sending Packets> %d", len(s.sendingPacketsMap)))
		return
	})
	// statistics TODO
}

type sortByStart []*Conn

func (s sortByStart) Len() int {
	return len(s)
}

func (s sortByStart) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

func (s sortByStart) Less(i, j int) bool {
	return s[i].start.After(s[j].start)
}

var strongPattern = regexp.MustCompile(`^<([^>]+)>`)

func (s *Session) handleHttp(w http.ResponseWriter) {
	w.Write([]byte(`
<html>
	<head>
	</head>
	<body>
		<p><a href="/">Home</a></p>
		`))

	for _, cb := range s.debugEntries {
		lines := cb()
		w.Write([]byte(`<p>`))
		for _, line := range lines {
			line = strongPattern.ReplaceAllString(line, `<strong>$1</strong>`)
			w.Write([]byte(line))
			w.Write([]byte(`<br />`))
		}
		w.Write([]byte(`</p>`))
	}

	w.Write([]byte(`
	</body>
</html>
		`))
}
