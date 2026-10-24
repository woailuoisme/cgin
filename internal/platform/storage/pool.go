package storage

import (
	"sync"
)

const copyBufferSize = 64 * 1024 // 64KB 高性能流复制缓冲

// copyBufferPool 提供统一复用的字节切片缓冲区，避免高并发大文件上传时产生大量短生命周期分配与 GC 压力。
var copyBufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, copyBufferSize)
		return &b
	},
}

// getCopyBuffer 从池中获取一个 64KB 临时缓冲区。
func getCopyBuffer() *[]byte {
	if b, ok := copyBufferPool.Get().(*[]byte); ok {
		return b
	}
	buf := make([]byte, copyBufferSize)
	return &buf
}

// putCopyBuffer 将 64KB 临时缓冲区归还至池中。
func putCopyBuffer(b *[]byte) {
	if b != nil && len(*b) == copyBufferSize {
		copyBufferPool.Put(b)
	}
}
