package jobs

import (
	"context"
	"testing"

	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/conversation"
	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/model"
)

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

func TestHangzhouFollowupUsesPriorJobQuery(t *testing.T) {
	ctx := conversation.WithTurns(context.Background(), []model.MessageTurn{{User: "你可以搜索一下虚拟化相关的工作岗位吗"}})
	query, location, ok := resolveSearch(ctx, "有没有杭州地区的")
	if !ok || query != "虚拟化" || location != "杭州" || !matchesLocation(location, "Hangzhou, China") ||
		matchesLocation(location, "Remote, China") {
		t.Fatalf("unexpected follow-up %q %q %t", query, location, ok)
	}
	if _, _, ok := resolveSearch(context.Background(), "有没有杭州地区的"); ok {
		t.Fatal("an unrelated Agent's location question inherited a job topic")
	}
}
