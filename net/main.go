package main

// main.go — コマンドライン
//
//   go run . node [flags] <app> [args...]
//
// 例 (端末 2 つで):
//   go run . node --mac 02:00:00:00:00:01 --ip 10.0.0.1 --wire-listen 127.0.0.1:9001 --wire-peer 127.0.0.1:9002 serve
//   go run . node --mac 02:00:00:00:00:02 --ip 10.0.0.2 --wire-listen 127.0.0.1:9002 --wire-peer 127.0.0.1:9001 ping 10.0.0.1

import (
	"bytes"
	"crypto/sha256"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

const usage = `usage: mininet node [flags] <app> [args]

apps:
  serve                         TCP echo :7, UDP echo :7, HTTP :80 を起動して待つ
  ping <ip> [count]             ICMP echo
  udp-echo <ip> <port> <msg>    UDP で送って返事を表示
  tcp-echo <ip> <port> <msg>    TCP echo サーバに送って返事を表示
  tcp-bulk <ip> <port> <bytes>  ランダムなバイト列を echo させて完全性を確認
  http-get <ip> <port> <path>   HTTP/1.0 GET

flags:
`

func main() {
	if len(os.Args) < 2 || os.Args[1] != "node" {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	fs := flag.NewFlagSet("node", flag.ExitOnError)
	macStr := fs.String("mac", "02:00:00:00:00:01", "このノードの MAC アドレス")
	ipStr := fs.String("ip", "10.0.0.1", "このノードの IPv4 アドレス")
	listen := fs.String("wire-listen", "127.0.0.1:9001", "仮想ケーブル: 自分が待ち受ける UDP アドレス")
	peer := fs.String("wire-peer", "127.0.0.1:9002", "仮想ケーブル: 相手の UDP アドレス")
	pcapPath := fs.String("pcap", "", "送受信フレームを書き出す .pcap ファイル")
	verbose := fs.Bool("v", false, "各層のログを表示")
	loss := fs.Float64("loss", 0, "送信フレームをこの確率で捨てる (例 0.1)。再送の観察用")
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage); fs.PrintDefaults() }
	fs.Parse(os.Args[2:])
	args := fs.Args()
	if len(args) == 0 {
		fs.Usage()
		os.Exit(2)
	}

	mac, err := ParseMAC(*macStr)
	check(err)
	ip, err := ParseIP(*ipStr)
	check(err)
	wire, err := NewUDPWire(*listen, *peer)
	check(err)
	var pcap *PcapWriter
	if *pcapPath != "" {
		pcap, err = NewPcapWriter(*pcapPath)
		check(err)
	}
	var dev LinkDevice = wire
	if *loss > 0 {
		dev = NewLossyLink(wire, *loss, time.Now().UnixNano())
	}
	s := NewStack(dev, mac, ip, pcap)
	if *verbose {
		s.Logf = StdLogf
	}
	defer func() {
		s.Close()
		pcap.Close()
	}()

	if err := runApp(s, args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		s.Close()
		pcap.Close()
		os.Exit(1)
	}
}

func runApp(s *Stack, args []string) error {
	arg := func(i int) string {
		if i >= len(args) {
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
		return args[i]
	}
	parseIP := func(i int) IP { ip, err := ParseIP(arg(i)); check(err); return ip }
	parsePort := func(i int) uint16 { p, err := strconv.ParseUint(arg(i), 10, 16); check(err); return uint16(p) }

	switch args[0] {
	case "serve":
		tl, err := s.ListenTCP(7)
		check(err)
		hl, err := s.ListenTCP(80)
		check(err)
		ul, err := s.ListenUDP(7)
		check(err)
		go ServeTCPEcho(tl)
		go ServeHTTP(hl)
		go ServeUDPEcho(ul)
		fmt.Printf("%v (%v) serving: tcp echo :7, udp echo :7, http :80. Ctrl-C to stop.\n", s.IP, s.MAC)
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		fmt.Println("shutting down")
		return nil

	case "ping":
		dst := parseIP(1)
		count := 4
		if len(args) > 2 {
			count, _ = strconv.Atoi(args[2])
		}
		ok := 0
		fmt.Printf("PING %v: 56 data bytes\n", dst)
		for i := 0; i < count; i++ {
			rtt, err := s.Ping(dst, uint16(i), 56, 2*time.Second)
			if err != nil {
				fmt.Printf("Request timeout for icmp_seq %d\n", i)
			} else {
				ok++
				fmt.Printf("64 bytes from %v: icmp_seq=%d time=%.3f ms\n", dst, i, float64(rtt.Microseconds())/1000)
			}
			time.Sleep(200 * time.Millisecond)
		}
		fmt.Printf("--- %v ping statistics ---\n%d packets transmitted, %d packets received\n", dst, count, ok)
		if ok == 0 {
			return fmt.Errorf("no reply")
		}

	case "udp-echo":
		u, err := s.ListenUDP(0)
		check(err)
		defer u.Close()
		dst, port := parseIP(1), parsePort(2)
		for try := 0; try < 3; try++ { // 最初は ARP 解決待ちなので数回試す
			u.WriteTo([]byte(arg(3)), dst, port)
			data, from, fport, err := u.ReadFrom(time.Second)
			if err == nil {
				fmt.Printf("reply from %v:%d: %q\n", from, fport, data)
				return nil
			}
		}
		return fmt.Errorf("no reply")

	case "tcp-echo":
		got, err := TCPEchoClient(s, parseIP(1), parsePort(2), []byte(arg(3)))
		if err != nil {
			return err
		}
		fmt.Printf("echo reply: %q\n", got)

	case "tcp-bulk":
		n, err := strconv.Atoi(arg(3))
		check(err)
		data := make([]byte, n)
		rand.Read(data)
		start := time.Now()
		got, err := TCPEchoClient(s, parseIP(1), parsePort(2), data)
		if err != nil {
			return err
		}
		el := time.Since(start)
		fmt.Printf("sent %d bytes, received %d bytes in %v\n", n, len(got), el.Round(time.Millisecond))
		fmt.Printf("sha256 sent=%x\nsha256 recv=%x\n", sha256.Sum256(data), sha256.Sum256(got))
		if !bytes.Equal(data, got) {
			return fmt.Errorf("data mismatch")
		}
		fmt.Println("integrity OK")

	case "http-get":
		resp, err := HTTPGet(s, parseIP(1), parsePort(2), arg(3))
		if err != nil {
			return err
		}
		fmt.Print(resp)

	default:
		return fmt.Errorf("unknown app %q", args[0])
	}
	time.Sleep(100 * time.Millisecond) // 最後の ACK を送り終えるための猶予
	return nil
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
