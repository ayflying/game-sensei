package memory

import "testing"

func TestBufferRingOverwrite(t *testing.T) {
	b := NewBuffer(3)
	for i := int64(1); i <= 5; i++ {
		b.Push(Record{FrameNo: i})
	}
	if got := b.Len(); got != 3 {
		t.Fatalf("Len()=%d, 期望 3（环形覆盖）", got)
	}
	snap := b.Snapshot(3)
	want := []int64{3, 4, 5}
	for i, r := range snap {
		if r.FrameNo != want[i] {
			t.Fatalf("Snapshot 顺序错误: got %v, want %v", snap, want)
		}
	}
}

func TestSnapshotPartial(t *testing.T) {
	b := NewBuffer(10)
	b.Push(Record{FrameNo: 1})
	b.Push(Record{FrameNo: 2})
	if len(b.Snapshot(5)) != 2 {
		t.Fatal("n 超过存量时应返回全部")
	}
	if len(b.Snapshot(0)) != 0 {
		t.Fatal("n=0 应返回空")
	}
}
