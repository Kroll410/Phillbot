package main

// Мінімальний WebSocket-клієнт (RFC 6455) лише на стандартній бібліотеці.

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

var testRoots *x509.CertPool

type wsConn struct {
	conn net.Conn
	br   *bufio.Reader
	wmu  sync.Mutex
}

func wsDial(raw string) (*wsConn, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	host := u.Host
	if u.Port() == "" {
		host += ":443"
	}
	d := &net.Dialer{Timeout: 15 * time.Second}
	conn, err := tls.DialWithDialer(d, "tcp", host, &tls.Config{ServerName: u.Hostname(), RootCAs: testRoots})
	if err != nil {
		return nil, err
	}
	kb := make([]byte, 16)
	rand.Read(kb)
	key := base64.StdEncoding.EncodeToString(kb)
	path := u.RequestURI()
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nUser-Agent: SubDayBot\r\n\r\n", path, u.Hostname(), key)
	conn.SetDeadline(time.Now().Add(15 * time.Second))
	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if resp.StatusCode != 101 {
		conn.Close()
		return nil, fmt.Errorf("websocket handshake: %s", resp.Status)
	}
	h := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	if resp.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(h[:]) {
		conn.Close()
		return nil, errors.New("websocket handshake: bad accept key")
	}
	conn.SetDeadline(time.Time{})
	return &wsConn{conn: conn, br: br}, nil
}

func (c *wsConn) SetReadDeadline(t time.Time) { c.conn.SetReadDeadline(t) }
func (c *wsConn) Close() error                { return c.conn.Close() }

func (c *wsConn) writeFrame(op byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	hdr := []byte{0x80 | op}
	n := len(payload)
	switch {
	case n < 126:
		hdr = append(hdr, 0x80|byte(n))
	case n < 65536:
		hdr = append(hdr, 0x80|126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 0x80|127)
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, uint64(n))
		hdr = append(hdr, b...)
	}
	mask := make([]byte, 4)
	rand.Read(mask)
	hdr = append(hdr, mask...)
	masked := make([]byte, n)
	for i := 0; i < n; i++ {
		masked[i] = payload[i] ^ mask[i%4]
	}
	c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := c.conn.Write(append(hdr, masked...))
	return err
}

// ReadMessage returns the next complete text/binary message, answering pings automatically.
func (c *wsConn) ReadMessage() ([]byte, error) {
	var msg []byte
	for {
		h := make([]byte, 2)
		if _, err := io.ReadFull(c.br, h); err != nil {
			return nil, err
		}
		fin := h[0]&0x80 != 0
		op := h[0] & 0x0f
		masked := h[1]&0x80 != 0
		n := uint64(h[1] & 0x7f)
		if n == 126 {
			b := make([]byte, 2)
			if _, err := io.ReadFull(c.br, b); err != nil {
				return nil, err
			}
			n = uint64(binary.BigEndian.Uint16(b))
		} else if n == 127 {
			b := make([]byte, 8)
			if _, err := io.ReadFull(c.br, b); err != nil {
				return nil, err
			}
			n = binary.BigEndian.Uint64(b)
		}
		if n > 16<<20 {
			return nil, errors.New("websocket frame too large")
		}
		var mk []byte
		if masked {
			mk = make([]byte, 4)
			if _, err := io.ReadFull(c.br, mk); err != nil {
				return nil, err
			}
		}
		p := make([]byte, n)
		if _, err := io.ReadFull(c.br, p); err != nil {
			return nil, err
		}
		if masked {
			for i := range p {
				p[i] ^= mk[i%4]
			}
		}
		switch op {
		case 0x8: // close
			c.writeFrame(0x8, nil)
			code := 0
			if len(p) >= 2 {
				code = int(binary.BigEndian.Uint16(p))
			}
			return nil, fmt.Errorf("сервер закрив з'єднання (код %d)", code)
		case 0x9: // ping
			c.writeFrame(0xA, p)
			continue
		case 0xA: // pong
			continue
		}
		msg = append(msg, p...)
		if fin {
			return msg, nil
		}
	}
}
