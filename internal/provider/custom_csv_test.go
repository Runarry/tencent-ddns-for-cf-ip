package provider

import (
	"math"
	"strings"
	"testing"
)

func TestParseCustomCSVSortsDedupesAndSkipsInvalidRows(t *testing.T) {
	input := "\ufeffIP 地址,已发送,已接收,丢包率,平均延迟,下载速度(MB/s),地区码\n" +
		"1.1.1.1,4,4,0.00,50.00,5.00,NRT\n" +
		"not-an-ip,4,4,0.00,20.00,100.00,NRT\n" +
		"2.2.2.2,4,4,0.00,30.00,bad,NRT\n" +
		"1.1.1.1,4,4,0.00,70.00,9.00,SIN\n" +
		"3.3.3.3,4,4,0.00,30.00,8.00,SIN\n" +
		"4.4.4.4,4,4,0.00,20.00,7.00,SIN\n"

	got, err := ParseCustomCSV(strings.NewReader(input), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("candidates = %#v", got)
	}
	if got[0].NodeID != CustomNodeID || got[0].IP != "1.1.1.1" || got[0].SpeedBPS != int64(math.Round(9*1024*1024)) {
		t.Fatalf("unexpected first candidate: %#v", got[0])
	}
	if got[1].IP != "3.3.3.3" || got[1].SpeedBPS != int64(math.Round(8*1024*1024)) {
		t.Fatalf("unexpected second candidate: %#v", got[1])
	}
}

func TestParseCustomCSVRequiresKnownHeader(t *testing.T) {
	_, err := ParseCustomCSV(strings.NewReader("IP,速度\n1.1.1.1,10\n"), 5)
	if err == nil {
		t.Fatal("expected header error")
	}
}
