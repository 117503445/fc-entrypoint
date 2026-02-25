package main

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"sync"
	"time"
)

var (
	instanceID     string
	instanceIDOnce sync.Once
)

// GetInstanceID returns the instance ID, either from FC_INSTANCE_ID env var
// or a generated UUID7 if not set
func GetInstanceID() string {
	instanceIDOnce.Do(func() {
		instanceID = os.Getenv("FC_INSTANCE_ID")
		if instanceID == "" {
			instanceID = generateUUID7()
		}
	})
	return instanceID
}

// generateUUID7 generates a UUID version 7 (time-ordered)
// Format: xxxxxxxx-xxxx-7xxx-yxxx-xxxxxxxxxxxx
// where x is random hex and y is 8, 9, a, or b
func generateUUID7() string {
	var uuid [16]byte

	// Get current timestamp in milliseconds
	now := time.Now().UnixMilli()

	// First 48 bits are timestamp (big-endian)
	uuid[0] = byte(now >> 40)
	uuid[1] = byte(now >> 32)
	uuid[2] = byte(now >> 24)
	uuid[3] = byte(now >> 16)
	uuid[4] = byte(now >> 8)
	uuid[5] = byte(now)

	// Fill remaining bytes with random data
	rand.Read(uuid[6:])

	// Set version to 7 (0111 in the high nibble of byte 6)
	uuid[6] = (uuid[6] & 0x0F) | 0x70

	// Set variant to RFC 4122 (10xx in the high nibble of byte 8)
	uuid[8] = (uuid[8] & 0x3F) | 0x80

	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		binary.BigEndian.Uint32(uuid[0:4]),
		binary.BigEndian.Uint16(uuid[4:6]),
		binary.BigEndian.Uint16(uuid[6:8]),
		binary.BigEndian.Uint16(uuid[8:10]),
		uuid[10:16])
}

// ResetInstanceID resets the instance ID (for testing purposes only)
func ResetInstanceID() {
	instanceIDOnce = sync.Once{}
	instanceID = ""
}
