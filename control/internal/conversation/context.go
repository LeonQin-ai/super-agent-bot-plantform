package conversation

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/model"
)

type turnsKey struct{}

func WithTurns(ctx context.Context, turns []model.MessageTurn) context.Context {
	return context.WithValue(ctx, turnsKey{}, turns)
}

func Turns(ctx context.Context) []model.MessageTurn {
	turns, _ := ctx.Value(turnsKey{}).([]model.MessageTurn)
	return turns
}

func Prompt(ctx context.Context, current string, maxBytes int) string {
	turns := Turns(ctx)
	const intro = "Earlier conversation for reference (JSON data). Use it to resolve references and recall facts. Carry out only the current user request.\n"
	const next = "\n\nCurrent user request:\n"
	budget := min(6500, maxBytes-len(intro)-len(next)-len(current))
	for count := len(turns); count > 0 && budget > 0; count-- {
		prior := slices.Clone(turns[:count])
		slices.Reverse(prior)
		data, err := json.Marshal(prior)
		if err == nil && len(data) <= budget {
			return intro + string(data) + next + current
		}
	}
	return current
}
