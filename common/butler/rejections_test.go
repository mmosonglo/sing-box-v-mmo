package butler

import (
	"encoding/json"
	"net/netip"
	"os"
	"testing"
	"time"
)

func TestRecordAndGetRejectionsWithClient(t *testing.T) {
	// Clean up log file if any
	_ = os.Remove(rejectionsLogFilePath)
	defer os.Remove(rejectionsLogFilePath)

	RecordRejection("192.168.3.191", "Vượt hạn ngạch", "Đã chạm trần 200 kết nối/thiết bị")
	RecordRejection("192.168.3.192", "Hết ngân sách kết nối", "Toàn mạng đạt 2600 kết nối")
	RecordRejection("Hệ thống", "Hết RAM khởi động", "RAM khả dụng chỉ còn 6MB")

	recs := GetRejections()
	if len(recs) < 3 {
		t.Fatalf("expected at least 3 rejections, got %d", len(recs))
	}

	last := recs[len(recs)-1]
	if last.Client != "Hệ thống" || last.Reason != "Hết RAM khởi động" {
		t.Fatalf("unexpected last record: %+v", last)
	}

	prev := recs[len(recs)-2]
	if prev.Client != "192.168.3.192" || prev.Reason != "Hết ngân sách kết nối" {
		t.Fatalf("unexpected prev record: %+v", prev)
	}

	first := recs[len(recs)-3]
	if first.Client != "192.168.3.191" || first.Reason != "Vượt hạn ngạch" {
		t.Fatalf("unexpected first record: %+v", first)
	}

	// Verify JSON serialization includes client field
	b, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("failed to marshal rejection record: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}
	if m["client"] != "192.168.3.191" {
		t.Fatalf("expected client '192.168.3.191' in JSON, got %v", m["client"])
	}
}

func TestRecordClientRejectionThrottled(t *testing.T) {
	ip := netip.MustParseAddr("192.168.3.199")
	slot := getSlot(ip)
	slot.LastRejectNano.Store(0)

	recordClientRejectionThrottled(slot, ip, "Vượt hạn ngạch", "Lần 1")
	firstTime := slot.LastRejectNano.Load()
	if firstTime == 0 {
		t.Fatal("expected LastRejectNano to be updated")
	}

	// Immediate second call should be throttled
	recordClientRejectionThrottled(slot, ip, "Vượt hạn ngạch", "Lần 2 (spam)")
	if slot.LastRejectNano.Load() != firstTime {
		t.Fatal("expected second call to be throttled")
	}

	// Simulate passage of 11 seconds
	slot.LastRejectNano.Store(time.Now().Add(-11 * time.Second).UnixNano())
	recordClientRejectionThrottled(slot, ip, "Vượt hạn ngạch", "Lần 3 (sau 11s)")
	if slot.LastRejectNano.Load() == firstTime {
		t.Fatal("expected third call after 11s to update LastRejectNano")
	}
}

func TestStandbyQueueTracking(t *testing.T) {
	_ = os.Remove(standbyQueueFilePath)
	defer os.Remove(standbyQueueFilePath)

	pid1 := 9991
	pid2 := 9992

	RecordStandbyState(pid1, "18:31:BF:1A:E6:83", "Proxy-US-1", "waiting", "RAM khả dụng còn 4MB, cần 5MB")
	RecordStandbyState(pid2, "192.168.3.192", "Proxy-US-2", "waiting", "RAM khả dụng còn 4MB, cần 5MB")

	q := GetStandbyQueue()
	if len(q) != 2 {
		t.Fatalf("expected 2 items in standby queue, got %d", len(q))
	}
	if q[0].Client != "18:31:BF:1A:E6:83" || q[1].Client != "192.168.3.192" {
		t.Fatalf("unexpected clients in standby queue: %+v", q)
	}

	// PID1 thức dậy
	RecordStandbyState(pid1, "18:31:BF:1A:E6:83", "Proxy-US-1", "waking", "Đủ RAM, tiến trình đang thức tỉnh")
	q2 := GetStandbyQueue()
	foundWaking := false
	for _, it := range q2 {
		if it.PID == pid1 && it.State == "waking" {
			foundWaking = true
		}
	}
	if !foundWaking {
		t.Fatal("expected pid1 to have state 'waking'")
	}

	// PID1 cất cánh xong, gỡ khỏi queue
	RemoveStandbyItem(pid1)
	q3 := GetStandbyQueue()
	if len(q3) != 1 || q3[0].PID != pid2 {
		t.Fatalf("expected 1 item left in standby queue (pid2), got %+v", q3)
	}

	// PID2 cũng gỡ
	RemoveStandbyItem(pid2)
	q4 := GetStandbyQueue()
	if len(q4) != 0 {
		t.Fatalf("expected empty standby queue, got %d", len(q4))
	}
}

