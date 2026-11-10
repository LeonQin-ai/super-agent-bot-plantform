package store

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/model"
)

// RecentMessageTurns returns the six latest completed message exchanges for
// the current Agent, most recent first. Execution output and failed tasks are excluded.
func (s *SQLite) RecentMessageTurns(ctx context.Context, current model.Task) ([]model.MessageTurn, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT payload,result FROM tasks
WHERE agent_id=? AND state='COMPLETED' AND (created_at,id)<(?,?)
 AND CASE WHEN json_valid(payload) THEN json_extract(payload,'$.source') IN ('feishu','api') ELSE 0 END
ORDER BY created_at DESC,id DESC LIMIT 6`, current.AgentID, current.CreatedAt, current.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var turns []model.MessageTurn
	for rows.Next() {
		var payload, answer string
		if err := rows.Scan(&payload, &answer); err != nil {
			return nil, err
		}
		var input struct{ Content string }
		if json.Unmarshal([]byte(payload), &input) == nil && strings.TrimSpace(input.Content) != "" {
			turns = append(turns, model.MessageTurn{User: clipTurn(input.Content, 1000), Assistant: clipTurn(answer, 1800)})
		}
	}
	return turns, rows.Err()
}

func clipTurn(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit] + "…"
}
