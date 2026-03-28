package domain

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"time"
)

// NewID generates a UUIDv7 string.
//
// UUIDv7 is time-sortable and lowercase-safe, making it suitable for both
// Postgres primary keys and Kubernetes label values. The format is:
//
//	xxxxxxxx-xxxx-7xxx-yxxx-xxxxxxxxxxxx
//
// where the first 48 bits encode a Unix timestamp in milliseconds, giving
// natural chronological ordering.
func NewID() string {
	var uuid [16]byte

	// Bytes 0-5: Unix timestamp in milliseconds (48 bits, big-endian).
	ms := uint64(time.Now().UnixMilli())
	binary.BigEndian.PutUint16(uuid[0:2], uint16(ms>>32))
	binary.BigEndian.PutUint16(uuid[2:4], uint16(ms>>16))
	binary.BigEndian.PutUint16(uuid[4:6], uint16(ms))

	// Bytes 6-15: random.
	_, _ = rand.Read(uuid[6:])

	// Set version (4 bits = 0b0111 → version 7).
	uuid[6] = (uuid[6] & 0x0F) | 0x70

	// Set variant (2 bits = 0b10 → RFC 9562).
	uuid[8] = (uuid[8] & 0x3F) | 0x80

	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		binary.BigEndian.Uint32(uuid[0:4]),
		binary.BigEndian.Uint16(uuid[4:6]),
		binary.BigEndian.Uint16(uuid[6:8]),
		binary.BigEndian.Uint16(uuid[8:10]),
		uuid[10:16],
	)
}
