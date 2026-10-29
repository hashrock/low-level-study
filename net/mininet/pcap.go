package mininet

// pcap.go — 送受信した全フレームを libpcap 形式 (.pcap) で書き出す。
// Wireshark / tcpdump -r でそのまま開ける。
//
// ファイル構造 (すべてリトルエンディアンで書く。magic で読み手が判別する):
//
//   グローバルヘッダ (24 bytes)
//     0  magic_number  uint32 = 0xa1b2c3d4 (マイクロ秒精度)
//     4  version_major uint16 = 2
//     6  version_minor uint16 = 4
//     8  thiszone      int32  = 0
//    12  sigfigs       uint32 = 0
//    16  snaplen       uint32 = 65535
//    20  network       uint32 = 1 (LINKTYPE_ETHERNET)
//
//   パケットレコード (16 bytes ヘッダ + フレーム本体) の繰り返し
//     0  ts_sec   uint32
//     4  ts_usec  uint32
//     8  incl_len uint32 (ファイルに保存した長さ)
//    12  orig_len uint32 (実際のフレーム長)

import (
	"encoding/binary"
	"io"
	"os"
	"sync"
	"time"
)

type PcapWriter struct {
	mu sync.Mutex
	w  io.Writer
	c  io.Closer // NewPcapWriter で開いたファイル (NewPcapWriterTo では nil)
}

func NewPcapWriter(path string) (*PcapWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	p, err := NewPcapWriterTo(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	p.c = f
	return p, nil
}

// NewPcapWriterTo は任意の io.Writer (bytes.Buffer など) に pcap を書き出す。
func NewPcapWriterTo(w io.Writer) (*PcapWriter, error) {
	hdr := make([]byte, 24)
	binary.LittleEndian.PutUint32(hdr[0:], 0xa1b2c3d4)
	binary.LittleEndian.PutUint16(hdr[4:], 2)
	binary.LittleEndian.PutUint16(hdr[6:], 4)
	binary.LittleEndian.PutUint32(hdr[16:], 65535)
	binary.LittleEndian.PutUint32(hdr[20:], 1)
	if _, err := w.Write(hdr); err != nil {
		return nil, err
	}
	return &PcapWriter{w: w}, nil
}

// WritePacket は 1 フレームを追記する。レコード単位で 1 回の write にまとめるので、
// プロセスが kill されてもファイルが壊れにくい。
func (p *PcapWriter) WritePacket(frame []byte) { p.WritePacketAt(time.Now(), frame) }

// WritePacketAt はタイムスタンプを指定して 1 フレームを追記する。
func (p *PcapWriter) WritePacketAt(now time.Time, frame []byte) {
	if p == nil {
		return
	}
	rec := make([]byte, 16+len(frame))
	binary.LittleEndian.PutUint32(rec[0:], uint32(now.Unix()))
	binary.LittleEndian.PutUint32(rec[4:], uint32(now.Nanosecond()/1000))
	binary.LittleEndian.PutUint32(rec[8:], uint32(len(frame)))
	binary.LittleEndian.PutUint32(rec[12:], uint32(len(frame)))
	copy(rec[16:], frame)
	p.mu.Lock()
	p.w.Write(rec)
	p.mu.Unlock()
}

func (p *PcapWriter) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.c == nil {
		return nil
	}
	return p.c.Close()
}
