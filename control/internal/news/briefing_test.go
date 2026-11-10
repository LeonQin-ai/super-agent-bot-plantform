package news

import (
	"strings"
	"testing"
)

func TestParseFeedKeepsUniqueArticleLinks(t *testing.T) {
	input := `<rss><channel>
	  <item><title>Headline</title><description><![CDATA[<p>Public details &amp; context.</p>]]></description><link>https://example.com/a</link></item>
	  <item><title>Duplicate</title><link>https://example.com/a</link></item>
	  <item><title>Invalid</title><link>file:///etc/passwd</link></item>
	</channel></rss>`
	items, err := parseFeed(strings.NewReader(input), "")
	if err != nil || len(items) != 1 || items[0].link != "https://example.com/a" || items[0].Description != "Public details & context." {
		t.Fatalf("unexpected feed: %+v, %v", items, err)
	}
}

func TestNaturalNewsQuestion(t *testing.T) {
	for _, prompt := range []string{"你可以抓取今天的新闻吗", "你可以抓取今天的新闻吗？", "今天的新闻", "帮我查一下今日新闻"} {
		category, today, matched := command(prompt)
		if category != "general" || !today || !matched {
			t.Errorf("question %q returned %q, today=%t, matched=%t", prompt, category, today, matched)
		}
	}
	if _, _, matched := command("帮我写一个抓取今天新闻的脚本"); matched {
		t.Fatal("an ordinary Agent request was routed to the news feed")
	}
}

func TestTodayFiltersByShanghaiPublicationDate(t *testing.T) {
	input := `<rss><channel>
	  <item><title>Yesterday</title><pubDate>Mon, 09 Nov 2026 15:59:00 +0000</pubDate><link>https://example.com/old</link></item>
	  <item><title>Today</title><pubDate>Mon, 09 Nov 2026 16:00:00 +0000</pubDate><link>https://example.com/today</link></item>
	  <item><title>Undated</title><link>https://example.com/undated</link></item>
	</channel></rss>`
	items, err := parseFeed(strings.NewReader(input), "2026-11-10")
	if err != nil || len(items) != 1 || items[0].Title != "Today" {
		t.Fatalf("unexpected dated feed: %+v, %v", items, err)
	}
}
