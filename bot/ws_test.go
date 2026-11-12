package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestWS(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Sec-WebSocket-Key")
		h := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		hj, _ := w.(http.Hijacker)
		c, brw, _ := hj.Hijack()
		defer c.Close()
		brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(h[:]) + "\r\n\r\n")
		brw.Flush()
		c.Write([]byte{0x89, 0x02, 'h', 'i'})                                           // ping
		big := strings.Repeat("я", 200)                                                 // 400 bytes -> 126 len
		c.Write(append([]byte{0x01, 126, byte(len(big) >> 8), byte(len(big))}, big...)) // non-fin text
		c.Write([]byte{0x80, 0x03, 'a', 'b', 'c'})                                      // fin continuation
		// read pong from client
		br := bufio.NewReader(c)
		hdr := make([]byte, 2)
		io.ReadFull(br, hdr)
		if hdr[0]&0x0f != 0xA {
			t.Errorf("expected pong, got %x", hdr[0])
		}
		mk := make([]byte, 4+2)
		io.ReadFull(br, mk)
		c.Write([]byte{0x88, 0x02, 0x03, 0xe8})
		io.ReadAll(br)
	}))
	srv.StartTLS()
	defer srv.Close()
	// trust test cert
	addr := srv.Listener.Addr().(*net.TCPAddr)
	_ = addr
	pool := srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	testRoots = pool
	ws, err := wsDial("wss://127.0.0.1:" + itoa(addr.Port) + "/ws")
	if err != nil {
		t.Fatal(err)
	}
	m, err := ws.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if string(m) != strings.Repeat("я", 200)+"abc" {
		t.Fatalf("bad msg %q", m)
	}
	_, err = ws.ReadMessage()
	if err == nil || !strings.Contains(err.Error(), "1000") {
		t.Fatalf("expected close 1000, got %v", err)
	}
}

func itoa(i int) string {
	return strings.TrimSpace(strings.Repeat(" ", 0)) + func() string {
		s := ""
		for i > 0 {
			s = string(rune('0'+i%10)) + s
			i /= 10
		}
		return s
	}()
}

func TestBadges(t *testing.T) {
	cases := []struct {
		b        []Badge
		tier, mo int
	}{
		{[]Badge{{"subscriber", "2006", "6"}}, 2, 6},
		{[]Badge{{"vip", "1", ""}, {"subscriber", "3012", "5"}}, 3, 5},
		{[]Badge{{"subscriber", "0", "1"}}, 1, 1},
		{[]Badge{{"founder", "0", "14"}}, 1, 14},
		{[]Badge{{"moderator", "1", ""}}, 0, 0},
	}
	for _, c := range cases {
		tr, mo := subFromBadges(c.b)
		if tr != c.tier || mo != c.mo {
			t.Errorf("%v -> %d,%d", c.b, tr, mo)
		}
	}
}

func TestMarbles(t *testing.T) {
	session = nil
	add := func(uid, name, reward, text string, pts int) {
		var ev Redemption
		ev.UserID, ev.UserName, ev.UserInput, ev.Reward.Title = uid, name, text, reward
		recordRedeem(ev, 1, pts, pts)
	}
	add("1", "Phill", "СабДей Замовлення", "Dark Souls", 138)
	add("2", "kroll", "СабДей Замовлення", "dark  souls!", 20)
	add("3", "x", "СабДей Замовлення", "Elden, Ring", 5)
	add("3", "x", "СабДей Замовлення", "Hades", 6) // replaces Elden Ring
	add("4", "y", "Гідратація", "pls", 50)         // other reward -> ignored
	g, used, f := aggregateSession()
	if !f || used != 3 || len(g) != 2 || g[0].points != 158 || g[1].name != "Hades" {
		t.Fatalf("%+v %d %v", g, used, f)
	}
	lines, total, scaled := marbleLines(g)
	if len(lines) != 164 || total != 164 || scaled {
		t.Fatal(len(lines), total)
	}
	big := []orderGroup{{name: "A,B", points: 5000}, {name: "C", points: 10}, {name: "D", points: 1}}
	l2, _, sc := marbleLines(big)
	cnt := map[string]int{}
	for _, x := range l2 {
		cnt[x]++
	}
	if !sc || len(l2) != 1000 || cnt["D"] != 1 || cnt["A B"] < 990 {
		t.Fatal(len(l2), cnt)
	}
	baseDir = t.TempDir()
	p, err := writeMarblesFile([]string{"Ой"}, "utf16")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if b[0] != 0xFF || b[1] != 0xFE {
		t.Fatal("bom")
	}
}
