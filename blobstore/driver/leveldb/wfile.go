package leveldb

import (
	"crypto/sha1"
	"encoding/binary"
)

const (
	chunkSize = 256 * 1024
)

var (
	littleEndian = binary.LittleEndian
)

type wfile struct {
	drv    *leveldbDriver
	id     string
	buf    []byte
	chunks [][]byte
	offset int
}

func (f *wfile) writeChunk() error {
	data := f.buf[:f.offset]
	hash := sha1.Sum(data)
	f.offset = 0
	if _, err := f.drv.chunks.Get(hash[:], nil); err == nil {
		// Chunk already known. Ignore errors != nil here, since
		// the worst thing that could happen could be overwriting
		// an existing chunk with the same data. If there was an error
		// reading the db, we'll get an error when putting the data
		// a few lines later.
		f.chunks = append(f.chunks, hash[:])
		return nil
	}
	// Not found,  write it
	if err := f.drv.chunks.Put(hash[:], data, nil); err != nil {
		return err
	}
	f.chunks = append(f.chunks, hash[:])
	return nil
}

func (f *wfile) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		c := copy(f.buf[f.offset:], p)
		f.offset += c
		n += c
		if f.offset == chunkSize {
			if err := f.writeChunk(); err != nil {
				return n, err
			}
		}
		p = p[c:]
	}
	return n, nil
}

func (f *wfile) Close() error {
	if f.offset > 0 {
		if len(f.chunks) == 0 {
			// Store the file inline. Data is uint32 + f.offset
			total := 4 + f.offset
			data := make([]byte, total)
			// 0 chunks indicates the data is inline
			littleEndian.PutUint32(data, uint32(0))
			copy(data[4:], f.buf)
			return f.drv.files.Put([]byte(f.id), data, nil)
		}
		if err := f.writeChunk(); err != nil {
			return err
		}
	}
	// Reserve n sha1 hashes + n uint32 + 1 uint32 (for the chunk count)
	total := (len(f.chunks) * (sha1.Size + 4)) + 4
	data := make([]byte, total)
	littleEndian.PutUint32(data, uint32(len(f.chunks)))
	pos := 4
	for _, chunk := range f.chunks {
		littleEndian.PutUint32(data[pos:], uint32(len(chunk)))
		pos += 4
		n := copy(data[pos:], chunk)
		pos += n
	}
	return f.drv.files.Put([]byte(f.id), data, nil)
}

func newWFile(drv *leveldbDriver, id string) *wfile {
	return &wfile{drv: drv, id: id, buf: make([]byte, chunkSize)}
}