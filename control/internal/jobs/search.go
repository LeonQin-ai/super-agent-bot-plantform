package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type AgentRun = func(context.Context, string, string, string) (string, error)

type board struct{ name, slug string }

var boards = []board{{"Canonical", "canonical"}, {"Cloudflare", "cloudflare"}, {"Datadog", "datadog"}}
var jobQuestion = regexp.MustCompile(`^(?:你可以|可以|能不能|帮我|请你|请)?(?:搜索|查找|查询|找|搜|查)(?:一下|下)?\s*(.+?)(?:相关)?(?:的)?\s*(?:工作岗位|招聘岗位|岗位|职位|工作)(?:吗|么|吧)?$`)
var virtualization = regexp.MustCompile(`(?i)virtuali[sz]ation|hypervisor|\bkvm\b|qemu|libvirt|vmware|openstack`)

var client = &http.Client{
	Timeout:       9 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func Wrap(next AgentRun) AgentRun {
	return func(ctx context.Context, task, workspace, prompt string) (string, error) {
		query, ok := queryFrom(prompt)
		if !ok {
			return next(ctx, task, workspace, prompt)
		}
		if query == "" || utf8.RuneCountInString(query) > 80 {
			return "请提供岗位关键词，例如「搜索虚拟化岗位」或「/jobs virtualization」。", nil
		}
		return search(ctx, query)
	}
}

func queryFrom(prompt string) (string, bool) {
	prompt = strings.TrimRight(strings.TrimSpace(prompt), "？?！!。")
	if prompt == "/jobs" {
		return "", true
	}
	if query, ok := strings.CutPrefix(prompt, "/jobs "); ok {
		return strings.TrimSpace(query), true
	}
	if match := jobQuestion.FindStringSubmatch(prompt); len(match) == 2 {
		return strings.TrimSpace(match[1]), true
	}
	return "", false
}

type vacancy struct {
	Title    string `json:"title"`
	Location struct {
		Name string `json:"name"`
	} `json:"location"`
	Link string `json:"absolute_url"`
}

type result struct {
	source board
	jobs   []vacancy
	err    error
}

func search(ctx context.Context, query string) (string, error) {
	results := make(chan result, len(boards))
	for _, source := range boards {
		go func(source board) {
			jobs, err := fetch(ctx, source)
			results <- result{source, jobs, err}
		}(source)
	}
	type match struct {
		vacancy
		company string
		rank    int
	}
	var matches []match
	var checked, unavailable []string
	for range boards {
		res := <-results
		if res.err != nil {
			unavailable = append(unavailable, res.source.name)
			continue
		}
		checked = append(checked, res.source.name)
		for _, job := range res.jobs {
			link, err := url.Parse(job.Link)
			if err != nil || link.Scheme != "https" || link.Hostname() == "" || link.User != nil || len(job.Link) > 512 {
				continue
			}
			if rank := matchTitle(query, job.Title); rank != 0 {
				matches = append(matches, match{job, res.source.name, rank})
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(checked) == 0 {
		return "", errors.New("recruiting sources unavailable")
	}
	sort.Strings(checked)
	sort.Strings(unavailable)
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].rank != matches[j].rank {
			return matches[i].rank > matches[j].rank
		}
		if matches[i].Title != matches[j].Title {
			return matches[i].Title < matches[j].Title
		}
		return matches[i].Link < matches[j].Link
	})
	var answer strings.Builder
	fmt.Fprintf(&answer, "「%s」岗位搜索\n已查阅雇主公开招聘板：%s。", query, strings.Join(checked, "、"))
	if len(unavailable) != 0 {
		fmt.Fprintf(&answer, "\n暂未能读取：%s。", strings.Join(unavailable, "、"))
	}
	if len(matches) == 0 {
		answer.WriteString("\n这些招聘板没有匹配的职位标题。可以试试英文职称或技术关键词。")
		return answer.String(), nil
	}
	seen := make(map[string]bool)
	for _, job := range matches {
		if seen[job.Link] {
			continue
		}
		seen[job.Link] = true
		fmt.Fprintf(&answer, "\n\n%d. %s · %s", len(seen), oneLine(job.Title), job.company)
		if location := oneLine(job.Location.Name); location != "" {
			fmt.Fprintf(&answer, "\n地点：%s", location)
		}
		fmt.Fprintf(&answer, "\n%s", job.Link)
		if len(seen) == 5 {
			break
		}
	}
	return answer.String(), nil
}

func matchTitle(query, title string) int {
	query, title = strings.ToLower(query), strings.ToLower(title)
	if strings.Contains(title, query) {
		return 3
	}
	switch query {
	case "虚拟化", "virtualization", "virtualisation":
		if strings.Contains(title, "virtualisation") || strings.Contains(title, "virtualization") {
			return 3
		}
		if virtualization.MatchString(title) {
			return 2
		}
	}
	return 0
}

func oneLine(s string) string {
	runes := []rune(strings.Join(strings.Fields(s), " "))
	if len(runes) > 180 {
		return string(runes[:180]) + "…"
	}
	return string(runes)
}

func fetch(ctx context.Context, source board) ([]vacancy, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://boards-api.greenhouse.io/v1/boards/"+source.slug+"/jobs", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "SuperAgentBot/1.0 (public job board reader)")
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("job board returned HTTP %d", response.StatusCode)
	}
	var body struct{ Jobs []vacancy }
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&body); err != nil {
		return nil, err
	}
	if body.Jobs == nil {
		return nil, errors.New("invalid job board response")
	}
	return body.Jobs, nil
}
