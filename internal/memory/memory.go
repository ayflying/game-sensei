// Package memory 提供轨迹缓冲（trajectory ring buffer）。
//
// 轨迹 (state摘要, action, 时间戳) 是「学生操作 → 老师查岗 → 学生再学」
// 飞轮的数据燃料。实时回路每帧写入；异步教学回路周期性批量取出回放给老师。
//
// Phase 0 只存轻量摘要（帧统计 + 动作 + 耗时），不存原始帧——原始帧体量大，
// 且 Phase 1 的「回放」再决定是否落盘截图。环形缓冲保证内存上限恒定。
package memory

import (
	"sync"
	"time"

	"github.com/ayflying/game-sensei/internal/agent"
)

// Record 是一帧的轨迹摘要。
type Record struct {
	FrameNo   int64         // 帧序号
	At        time.Time     // 采集时刻
	CaptureMs float64       // 截屏耗时
	DecideMs  float64       // 决策耗时
	InputMs   float64       // 输入耗时
	TotalMs   float64       // 单帧总耗时
	Action    agent.Action  // 输出的动作
	GrayMean  uint8         // 帧平均亮度（摘要，非原始像素）
}

// Buffer 是并发安全的环形轨迹缓冲。
type Buffer struct {
	mu    sync.Mutex
	buf   []Record
	size  int // 容量
	next  int // 下一个写入位置
	count int // 已写入总数（未满时等于长度）
}

// NewBuffer 构造容量为 size 的环形缓冲。
func NewBuffer(size int) *Buffer {
	if size <= 0 {
		size = 1024
	}
	return &Buffer{buf: make([]Record, size), size: size}
}

// Push 写入一条轨迹，满则覆盖最旧。
func (b *Buffer) Push(r Record) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf[b.next] = r
	b.next = (b.next + 1) % b.size
	if b.count < b.size {
		b.count++
	}
}

// Snapshot 返回最近 n 条轨迹（按时间升序）。n 大于已存数量时返回全部。
func (b *Buffer) Snapshot(n int) []Record {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > b.count {
		n = b.count
	}
	out := make([]Record, 0, n)
	start := (b.next - n + b.size*2) % b.size
	for i := 0; i < n; i++ {
		out = append(out, b.buf[(start+i)%b.size])
	}
	return out
}

// Len 返回当前缓冲内轨迹条数。
func (b *Buffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count
}
