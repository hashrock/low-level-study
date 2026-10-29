package minidb

import "io"

// MemFile はメモリ上のバイト列を File として見せる (ブラウザ版やテスト用)。
// 中身は普通の DB ファイルと同じなので、Bytes() をそのまま保存すれば CLI で開ける。
type MemFile struct {
	data []byte
}

func NewMemFile(data []byte) *MemFile { return &MemFile{data: data} }

func (m *MemFile) ReadAt(b []byte, off int64) (int, error) {
	if off >= int64(len(m.data)) {
		return 0, io.EOF
	}
	n := copy(b, m.data[off:])
	if n < len(b) {
		return n, io.EOF
	}
	return n, nil
}

func (m *MemFile) WriteAt(b []byte, off int64) (int, error) {
	if end := off + int64(len(b)); end > int64(len(m.data)) {
		m.data = append(m.data, make([]byte, end-int64(len(m.data)))...)
	}
	return copy(m.data[off:], b), nil
}

func (m *MemFile) Sync() error  { return nil }
func (m *MemFile) Close() error { return nil }

// Bytes はファイルの中身 (共有されたスライス) を返す。
func (m *MemFile) Bytes() []byte { return m.data }
