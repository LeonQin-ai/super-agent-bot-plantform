package jobs

import "testing"

func TestScreenshotQuestion(t *testing.T) {
	query, ok := queryFrom("你可以搜索一下虚拟化相关的工作岗位吗？")
	if !ok || query != "虚拟化" {
		t.Fatalf("query = %q, matched=%t", query, ok)
	}
	if matchTitle(query, "Containerization & Virtualisation Engineer") != 3 ||
		matchTitle(query, "Software Engineer - OpenStack") != 2 ||
		matchTitle(query, "General Software Engineer") != 0 {
		t.Fatal("virtualization matching failed")
	}
	if _, ok := queryFrom("写一个搜索虚拟化岗位的脚本"); ok {
		t.Fatal("job search intercepted a normal task")
	}
}
