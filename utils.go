package main

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"unsafe"
)

func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func generateSeededData(size int, seed uint32) []byte {
	data := make([]byte, size)
	for i := 0; i < size; i += 4 {
		binary.LittleEndian.PutUint32(data[i:i+4], seed+uint32(i))
	}
	return data
}

func generateSectorPattern(sectorIndex, sectorSize int) []byte {
	pattern := make([]byte, sectorSize)
	for i := 0; i < sectorSize; i++ {
		pattern[i] = byte((sectorIndex*sectorSize + i) % 256)
	}
	return pattern
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func randIntn(n int) int {
	if n <= 0 {
		return 0
	}
	return rand.Intn(n)
}

func alignedBuffer(size, align int) []byte {
	buf := make([]byte, size+align)
	a := uintptr(align)
	offset := a - (uintptr(unsafe.Pointer(&buf[0])) % a)
	if int(offset) == align {
		offset = 0
	}
	return buf[offset : offset+uintptr(size)]
}

func randPerm(n int) []int {
	p := make([]int, n)
	for i := range p {
		p[i] = i
	}
	for i := n - 1; i > 0; i-- {
		j := rand.Intn(i + 1)
		p[i], p[j] = p[j], p[i]
	}
	return p
}