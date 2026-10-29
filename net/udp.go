package main

// udp.go — UDP と最小限のソケット API
//
//   0             2             4             6             8
//   +-------------+-------------+-------------+-------------+-----------+
//   | Src Port    | Dst Port    | Length      | Checksum    | Data ...  |
//   +-------------+-------------+-------------+-------------+-----------+
//   Length = ヘッダ(8) + データ長。Checksum は疑似ヘッダ込み (0 なら「計算なし」の意味)。

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

type udpDatagram struct {
	from IP
	port uint16
	data []byte
}

type UDPConn struct {
	s      *Stack
	port   uint16
	rx     chan udpDatagram
	closed chan struct{}
}

var ErrClosed = errors.New("use of closed connection")

func (s *Stack) handleUDP(ip IPv4Header, b []byte) {
	if len(b) < 8 {
		return
	}
	length := int(binary.BigEndian.Uint16(b[4:6]))
	if length < 8 || length > len(b) {
		return
	}
	b = b[:length]
	if binary.BigEndian.Uint16(b[6:8]) != 0 && pseudoHeaderChecksum(ip.Src, ip.Dst, ProtoUDP, b) != 0 {
		s.logf("udp: bad checksum")
		return
	}
	src := binary.BigEndian.Uint16(b[0:2])
	dst := binary.BigEndian.Uint16(b[2:4])
	s.mu.Lock()
	sock := s.udpSocks[dst]
	s.mu.Unlock()
	if sock == nil {
		return // 本来は ICMP Port Unreachable を返す
	}
	select {
	case sock.rx <- udpDatagram{from: ip.Src, port: src, data: append([]byte(nil), b[8:]...)}:
	default: // 受信キューが一杯なら捨てる (UDP なので許される)
	}
}

// ListenUDP はポートを開く。
func (s *Stack) ListenUDP(port uint16) (*UDPConn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if port == 0 {
		port = s.allocPortLocked()
	}
	if _, used := s.udpSocks[port]; used {
		return nil, fmt.Errorf("udp: port %d in use", port)
	}
	c := &UDPConn{s: s, port: port, rx: make(chan udpDatagram, 64), closed: make(chan struct{})}
	s.udpSocks[port] = c
	return c, nil
}

func (c *UDPConn) LocalPort() uint16 { return c.port }

// ReadFrom はデータグラムを 1 つ受け取る。timeout が 0 なら無期限に待つ。
func (c *UDPConn) ReadFrom(timeout time.Duration) ([]byte, IP, uint16, error) {
	var tc <-chan time.Time
	if timeout > 0 {
		tc = time.After(timeout)
	}
	select {
	case d := <-c.rx:
		return d.data, d.from, d.port, nil
	case <-c.closed:
		return nil, IP{}, 0, ErrClosed
	case <-tc:
		return nil, IP{}, 0, ErrTimeout
	}
}

func (c *UDPConn) WriteTo(data []byte, dst IP, port uint16) error {
	b := make([]byte, 8+len(data))
	binary.BigEndian.PutUint16(b[0:2], c.port)
	binary.BigEndian.PutUint16(b[2:4], port)
	binary.BigEndian.PutUint16(b[4:6], uint16(len(b)))
	copy(b[8:], data)
	sum := pseudoHeaderChecksum(c.s.IP, dst, ProtoUDP, b)
	if sum == 0 {
		sum = 0xffff // 0 は「チェックサムなし」と区別するため 0xffff で送る
	}
	binary.BigEndian.PutUint16(b[6:8], sum)
	return c.s.SendIP(dst, ProtoUDP, b)
}

func (c *UDPConn) Close() error {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if c.s.udpSocks[c.port] == c {
		delete(c.s.udpSocks, c.port)
		close(c.closed)
	}
	return nil
}

// allocPortLocked はエフェメラルポートを払い出す (s.mu を持った状態で呼ぶ)。
func (s *Stack) allocPortLocked() uint16 {
	for {
		s.nextPort++
		if s.nextPort < 49152 {
			s.nextPort = 49152
		}
		p := s.nextPort
		if _, ok := s.udpSocks[p]; ok {
			continue
		}
		used := false
		for k := range s.tcpConns {
			if k.localPort == p {
				used = true
				break
			}
		}
		if !used {
			return p
		}
	}
}
