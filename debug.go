package van

import (
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"regexp"
	"sort"
)

func (s *Session) setDebugEntries() {
	// statistics
	s.AddDebugEntry(func() (ret []string) {
		ret = append(ret, fmt.Sprintf("<In / Out> %s / %s", formatBytes(s.inBytes), formatBytes(s.outBytes)))
		return
	})
	// sending packets
	s.AddDebugEntry(func() (ret []string) {
		ret = append(ret, fmt.Sprintf("<Sending Packets> %d", len(s.sendingPacketsMap)))
		return
	})
	// incoming packets
	s.AddDebugEntry(func() (ret []string) {
		ret = append(ret, fmt.Sprintf("<Incoming Packets> %d", len(s.incomingPacketsMap)))
		return
	})
	// resent stat
	s.AddDebugEntry(func() (ret []string) {
		ret = append(ret, fmt.Sprintf("<Resend> %d %s", s.resentPackets, formatBytes(s.resentBytes)))
		return
	})
	// connections
	s.AddDebugEntry(func() (ret []string) {
		ret = append(ret, fmt.Sprintf("<Connections> %d", len(s.conns)))
		conns := make([]*Conn, 0, len(s.conns))
		for _, conn := range s.conns {
			conns = append(conns, conn)
		}
		sort.Sort(sortByStart(conns))
		for _, conn := range conns {
			ret = append(ret, fmt.Sprintf("%02d:%02d:%02d id %d serial %d out %s ackSerial %d in %s",
				conn.start.Hour(), conn.start.Minute(), conn.start.Second(),
				conn.Id, conn.serial, formatBytes(conn.outBytes), conn.ackSerial, formatBytes(conn.inBytes)))
		}
		return
	})
	s.AddDebugEntry(func() (ret []string) {
		line := "<Closed Connection Ids>"
		for e := s.closedConnIds.Front(); e != nil; e = e.Next() {
			r := e.Value.(*connIdRange)
			line += fmt.Sprintf(" %d-%d", r.left, r.right)
		}
		ret = append(ret, line)
		return
	})
	// transports
	s.AddDebugEntry(func() (ret []string) {
		ret = append(ret, "<Transports>")
		for _, t := range s.transports {
			ret = append(ret, fmt.Sprintf("%s", t.RemoteAddr().String()))
		}
		return
	})
}

func formatBytes(n int) string {
	units := "bKMGTP"
	i := 0
	result := ""
	for n > 0 {
		res := n % 1024
		if res > 0 {
			result = fmt.Sprintf("%d%c", res, units[i]) + result
		}
		n /= 1024
		i++
	}
	return result
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
