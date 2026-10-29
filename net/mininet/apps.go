package mininet

// apps.go — 自作スタックの上で動くデモアプリ (echo / HTTP/1.0)

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"time"
)

// ServeTCPEcho は受け取ったバイトをそのまま送り返す。相手が閉じたらこちらも閉じる。
func ServeTCPEcho(l *TCPListener) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go func() {
			n, _ := io.Copy(c, c)
			c.s.logf("echo: %s done, %d bytes", c.RemoteAddr(), n)
			c.Close()
		}()
	}
}

// TCPEchoClient は data を送り、返ってきたバイト列を返す。
// 書き込みと読み込みを並行に行わないと、相手の送信が自分の受信ウィンドウで詰まる。
func TCPEchoClient(s *Stack, dst IP, port uint16, data []byte) ([]byte, error) {
	c, err := s.DialTCP(dst, port)
	if err != nil {
		return nil, err
	}
	werr := make(chan error, 1)
	go func() {
		_, err := c.Write(data)
		c.Close() // 送信完了 → FIN
		werr <- err
	}()
	got, err := io.ReadAll(c)
	if err != nil {
		return got, err
	}
	return got, <-werr
}

func ServeUDPEcho(u *UDPConn) {
	for {
		data, from, port, err := u.ReadFrom(0)
		if err != nil {
			return
		}
		u.WriteTo(data, from, port)
	}
}

// ServeHTTP はとても小さな HTTP/1.0 サーバ。1 リクエストごとにコネクションを閉じる。
func ServeHTTP(l *TCPListener) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			var req []byte
			buf := make([]byte, 1024)
			for !bytes.Contains(req, []byte("\r\n\r\n")) && len(req) < 8192 {
				n, err := c.Read(buf)
				if err != nil {
					return
				}
				req = append(req, buf[:n]...)
			}
			line, _, _ := strings.Cut(string(req), "\r\n")
			parts := strings.Fields(line)
			if len(parts) < 2 || parts[0] != "GET" {
				io.WriteString(c, "HTTP/1.0 400 Bad Request\r\n\r\n")
				return
			}
			body := fmt.Sprintf("<html><body><h1>Hello from mininet!</h1>\n<p>path=%s</p>\n<p>server=%v time=%s</p></body></html>\n",
				parts[1], c.s.IP, time.Now().Format(time.RFC3339))
			fmt.Fprintf(c, "HTTP/1.0 200 OK\r\nContent-Type: text/html\r\nContent-Length: %d\r\nServer: mininet\r\n\r\n%s", len(body), body)
		}()
	}
}

// HTTPGet は GET を送り、レスポンス全体 (ヘッダ込み) を返す。
func HTTPGet(s *Stack, dst IP, port uint16, path string) (string, error) {
	c, err := s.DialTCP(dst, port)
	if err != nil {
		return "", err
	}
	defer c.Close()
	fmt.Fprintf(c, "GET %s HTTP/1.0\r\nHost: %v\r\n\r\n", path, dst)
	resp, err := io.ReadAll(c)
	return string(resp), err
}
