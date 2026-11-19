package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/model"
)

type Schedule struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Minutes int64  `json:"minutes"`
	NextRun int64  `json:"next_run"`
}

func (s *SQLite) Agents(ctx context.Context, after string) ([]model.Agent, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,tenant_id,state,last_active_at FROM agents WHERE id>? ORDER BY id LIMIT 100", after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	agents := make([]model.Agent, 0)
	for rows.Next() {
		var a model.Agent
		if err := rows.Scan(&a.ID, &a.TenantID, &a.State, &a.LastActiveAt); err != nil {
			return nil, err
		}
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

func (s *SQLite) Tasks(ctx context.Context, id string) ([]model.Task, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,agent_id,request_id,state,payload,result,error_code,created_at,updated_at FROM tasks
 WHERE agent_id=? ORDER BY created_at DESC,id DESC LIMIT 20`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := make([]model.Task, 0)
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

type executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func broadcast(ctx context.Context, db executor, request, content string) (int64, error) {
	payload, _ := json.Marshal(map[string]string{"source": "api", "content": content})
	now := timestamp()
	result, err := db.ExecContext(ctx, `INSERT INTO tasks(id,agent_id,request_id,state,payload,created_at,updated_at)
 SELECT 'tsk_'||lower(hex(randomblob(16))),id,?,'QUEUED',?,?,? FROM agents WHERE true
 ON CONFLICT(agent_id,request_id) DO NOTHING`, request, string(payload), now, now)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *SQLite) Broadcast(ctx context.Context, request, content string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var previous string
	err = tx.QueryRowContext(ctx, "SELECT content FROM broadcasts WHERE request_id=?", request).Scan(&previous)
	if err == nil {
		if previous == content {
			return 0, nil
		}
		return 0, ErrConflict
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO broadcasts(request_id,content) VALUES(?,?)", request, content); err != nil {
		return 0, err
	}
	count, err := broadcast(ctx, tx, "batch:"+request, content)
	if err != nil {
		return 0, err
	}
	return count, tx.Commit()
}

func (s *SQLite) Schedules(ctx context.Context) ([]Schedule, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,content,minutes,next_run FROM schedules ORDER BY next_run,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Schedule, 0)
	for rows.Next() {
		var item Schedule
		if err := rows.Scan(&item.ID, &item.Content, &item.Minutes, &item.NextRun); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLite) AddSchedule(ctx context.Context, item Schedule) (Schedule, error) {
	item.ID = newID("sch")
	_, err := s.db.ExecContext(ctx, "INSERT INTO schedules(id,content,minutes,next_run) VALUES(?,?,?,?)", item.ID, item.Content, item.Minutes, item.NextRun)
	return item, err
}

func (s *SQLite) RemoveSchedule(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM schedules WHERE id=?", id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return ErrNotFound
	}
	return err
}

// QueueDueSchedule coalesces missed occurrences and preserves cadence.
func (s *SQLite) QueueDueSchedule(ctx context.Context, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var item Schedule
	err = tx.QueryRowContext(ctx, "SELECT id,content,minutes,next_run FROM schedules WHERE next_run<=? ORDER BY next_run,id LIMIT 1", now.Unix()).Scan(&item.ID, &item.Content, &item.Minutes, &item.NextRun)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	key := item.ID + ":" + time.Unix(item.NextRun, 0).UTC().Format(time.RFC3339)
	if _, err := broadcast(ctx, tx, key, item.Content); err != nil {
		return err
	}
	interval := item.Minutes * 60
	next := item.NextRun + ((now.Unix()-item.NextRun)/interval+1)*interval
	if _, err := tx.ExecContext(ctx, "UPDATE schedules SET next_run=? WHERE id=?", next, item.ID); err != nil {
		return err
	}
	return tx.Commit()
}
