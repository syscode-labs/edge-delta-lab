package lab

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
)

// This small Gear-style CDC implementation is independently written for the lab.
// It is NOT desync/casync/FastCDC wire-compatible. The versioned table and boundary
// rule are fixed here; the cryptographic content identity is SHA-256, not Gear.
var gearTable = func() [256]uint64 {
	var t [256]uint64
	for i := range t {
		h := sha256.Sum256([]byte(fmt.Sprintf("edge-delta-lab/gear-v1/%d", i)))
		t[i] = binary.LittleEndian.Uint64(h[:8])
	}
	return t
}()

func Split(r io.Reader, min, avg, max int, emit func([]byte) error) error {
	if min < 64 || min > avg || avg > max || max > MaxChunkBytes || avg&(avg-1) != 0 {
		return fmt.Errorf("invalid CDC parameters")
	}
	br := bufio.NewReaderSize(r, 256<<10)
	buf := make([]byte, 0, max)
	var rolling uint64
	for {
		b, e := br.ReadByte()
		if e == io.EOF {
			if len(buf) > 0 {
				return emit(buf)
			}
			return nil
		}
		if e != nil {
			return e
		}
		buf = append(buf, b)
		rolling = (rolling << 1) + gearTable[b]
		if len(buf) >= max || (len(buf) >= min && rolling&uint64(avg-1) == 0) {
			if e = emit(buf); e != nil {
				return e
			}
			buf = buf[:0]
			rolling = 0
		}
	}
}
