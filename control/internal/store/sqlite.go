package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/model"
	_ "modernc.org/sqlite"
)

type SQLite struct { db *sql.DB }

func Open(path string) (*SQLite, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil { return nil, fmt.Errorf("open sqlite: %w", err) }
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &SQLite{db: db}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON; PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configure sqlite: %w", err)
	}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLite) migrate(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS agents (
 id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, role TEXT NOT NULL, state TEXT NOT NULL,
 memory_id TEXT NOT NULL, session_id TEXT NOT NULL, workspace_id TEXT NOT NULL,
 generation INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 last_active_at TEXT NOT NULL, external_identity TEXT UNIQUE
);
CREATE TABLE IF NOT EXISTS tasks (
 id TEXT PRIMARY KEY, agent_id TEXT NOT NULL REFERENCES agents(id), request_id TEXT NOT NULL,
 state TEXT NOT NULL, payload TEXT NOT NULL, result TEXT NOT NULL DEFAULT '',
 error_code TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 UNIQUE(agent_id, request_id)
);
CREATE INDEX IF NOT EXISTS idx_tasks_state_created ON tasks(state, created_at);
CREATE TABLE IF NOT EXISTS event_inbox (
 event_id TEXT PRIMARY KEY, agent_id TEXT NOT NULL REFERENCES agents(id),
 task_id TEXT NOT NULL REFERENCES tasks(id), source TEXT NOT NULL DEFAULT 'feishu',
 received_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS runtime_instances (
 agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
 runtime_id TEXT NOT NULL DEFAULT '', generation INTEGER NOT NULL DEFAULT 0,
 state TEXT NOT NULL DEFAULT 'ABSENT', endpoint TEXT NOT NULL DEFAULT '',
 last_error TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_runtime_id ON runtime_instances(runtime_id) WHERE runtime_id <> '';
CREATE TABLE IF NOT EXISTS capability_observations (
 version INTEGER PRIMARY KEY AUTOINCREMENT,
 recorded_at TEXT NOT NULL, snapshot_json TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS task_workers (
 task_id TEXT PRIMARY KEY REFERENCES tasks(id), container_id TEXT NOT NULL UNIQUE
);
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate sqlite: %w", err)
	}
	// CREATE TABLE IF NOT EXISTS does not evolve databases created by earlier
	// M1 builds. Add columns that newer queries require without dropping data.
	if err := s.ensureColumn(ctx, "tasks", "error_code", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "event_inbox", "source", "TEXT NOT NULL DEFAULT 'feishu'"); err != nil {
		return err
	}
	return nil
}

func (s *SQLite) ensureColumn(ctx context.Context, table, column, definition string) error {
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil { return fmt.Errorf("inspect %s schema: %w", table, err) }
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("read %s schema: %w", table, err)
		}
		if name == column { found = true; break }
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate %s schema: %w", table, err)
	}
	if err := rows.Close(); err != nil { return fmt.Errorf("close %s schema query: %w", table, err) }
	if found { return nil }
	if _, err := s.db.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column+" "+definition); err != nil {
		return fmt.Errorf("add %s.%s: %w", table, column, err)
	}
	return nil
}

func (s *SQLite) CreateAgent(ctx context.Context, tenantID, role string) (model.Agent, error) {
	if tenantID == "" { return model.Agent{}, errors.New("tenant_id is required") }
	if role == "" { role = "supervisor" }
	if role != "supervisor" { return model.Agent{}, errors.New("only supervisor identities may be created through the public API") }
	now, id := timestamp(), newID("agt")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { return model.Agent{}, err }
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO agents
(id,tenant_id,role,state,memory_id,session_id,workspace_id,generation,created_at,updated_at,last_active_at)
VALUES(?,?,?,'IDLE',?,?,?,0,?,?,?)`,
		id, tenantID, role, newID("mem"), newID("ses"), newID("wsp"), now, now, now)
	if err != nil { return model.Agent{}, fmt.Errorf("insert agent: %w", err) }
	if _, err = tx.ExecContext(ctx, "INSERT INTO runtime_instances(agent_id,state,generation,created_at,updated_at) VALUES(?,'ABSENT',0,?,?)", id, now, now); err != nil {
		return model.Agent{}, err
	}
	if err = tx.Commit(); err != nil { return model.Agent{}, err }
	return s.GetAgent(ctx, id)
}

func (s *SQLite) GetAgent(ctx context.Context, id string) (model.Agent, error) {
	var a model.Agent
	var state string
	err := s.db.QueryRowContext(ctx, `SELECT id,tenant_id,role,state,memory_id,session_id,workspace_id,
generation,created_at,updated_at,last_active_at FROM agents WHERE id=?`, id).Scan(
		&a.ID,&a.TenantID,&a.Role,&state,&a.MemoryID,&a.SessionID,&a.WorkspaceID,
		&a.Generation,&a.CreatedAt,&a.UpdatedAt,&a.LastActiveAt)
	if errors.Is(err, sql.ErrNoRows) { return model.Agent{}, ErrNotFound }
	if err != nil { return model.Agent{}, fmt.Errorf("get agent: %w", err) }
	a.State = model.AgentState(state)
	return a, nil
}

func (s *SQLite) CreateTask(ctx context.Context, agentID, requestID, payload string) (model.Task, error) {
	if _, err := s.GetAgent(ctx, agentID); err != nil { return model.Task{}, err }
	if requestID == "" { requestID = newID("req") }
	if payload == "" { payload = "{}" }
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { return model.Task{}, err }
	defer tx.Rollback()
	now := timestamp()
	_, err = tx.ExecContext(ctx, `INSERT INTO tasks
(id,agent_id,request_id,state,payload,created_at,updated_at)
VALUES(?,?,?,'QUEUED',?,?,?) ON CONFLICT(agent_id,request_id) DO NOTHING`,
		newID("tsk"),agentID,requestID,payload,now,now)
	if err != nil { return model.Task{}, fmt.Errorf("insert task: %w", err) }
	task, err := taskByRequest(ctx, tx, agentID, requestID)
	if err != nil { return model.Task{}, err }
	if task.Payload != payload { return model.Task{}, ErrConflict }
	if err = tx.Commit(); err != nil { return model.Task{}, err }
	return task, nil
}

type queryer interface { QueryRowContext(context.Context, string, ...any) *sql.Row }

func taskByRequest(ctx context.Context, q queryer, agentID, requestID string) (model.Task, error) {
	return scanTask(q.QueryRowContext(ctx, `SELECT id,agent_id,request_id,state,payload,result,error_code,created_at,updated_at
FROM tasks WHERE agent_id=? AND request_id=?`, agentID, requestID))
}

func (s *SQLite) GetTask(ctx context.Context, id string) (model.Task, error) {
	return scanTask(s.db.QueryRowContext(ctx, `SELECT id,agent_id,request_id,state,payload,result,error_code,created_at,updated_at
FROM tasks WHERE id=?`, id))
}

func scanTask(row *sql.Row) (model.Task, error) {
	var t model.Task
	var state string
	err := row.Scan(&t.ID,&t.AgentID,&t.RequestID,&state,&t.Payload,&t.Result,&t.ErrorCode,&t.CreatedAt,&t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) { return model.Task{}, ErrNotFound }
	if err != nil { return model.Task{}, fmt.Errorf("scan task: %w", err) }
	t.State = model.TaskState(state)
	return t, nil
}

func (s *SQLite) ClaimNextTask(ctx context.Context) (model.Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { return model.Task{}, err }
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, "SELECT id FROM tasks WHERE state='QUEUED' ORDER BY created_at,id LIMIT 1").Scan(&id)
	if errors.Is(err, sql.ErrNoRows) { return model.Task{}, ErrNotFound }
	if err != nil { return model.Task{}, err }
	res, err := tx.ExecContext(ctx, "UPDATE tasks SET state='RUNNING',updated_at=? WHERE id=? AND state='QUEUED'", timestamp(), id)
	if err != nil { return model.Task{}, err }
	n, err := res.RowsAffected()
	if err != nil || n != 1 { return model.Task{}, ErrConflict }
	if _, err = tx.ExecContext(ctx, "UPDATE agents SET state='ACTIVE',updated_at=? WHERE id=(SELECT agent_id FROM tasks WHERE id=?)", timestamp(), id); err != nil { return model.Task{}, err }
	task, err := scanTask(tx.QueryRowContext(ctx, `SELECT id,agent_id,request_id,state,payload,result,error_code,created_at,updated_at
FROM tasks WHERE id=?`, id))
	if err != nil { return model.Task{}, err }
	if err = tx.Commit(); err != nil { return model.Task{}, err }
	return task, nil
}

func (s *SQLite) CompleteTask(ctx context.Context, id, resultText string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { return err }
	defer tx.Rollback()
	now := timestamp()
	res, err := tx.ExecContext(ctx, "UPDATE tasks SET state='COMPLETED',result=?,updated_at=? WHERE id=? AND state='RUNNING'", resultText, now, id)
	if err != nil { return err }
	n, err := res.RowsAffected()
	if err != nil { return err }
	if n != 1 { return ErrConflict }
	var agentID string
	if err = tx.QueryRowContext(ctx, "SELECT agent_id FROM tasks WHERE id=?", id).Scan(&agentID); err != nil { return err }
	if _, err = tx.ExecContext(ctx, `UPDATE agents SET state='IDLE',updated_at=?,last_active_at=?
WHERE id=? AND state='ACTIVE'
AND NOT EXISTS (SELECT 1 FROM tasks WHERE agent_id=? AND state='RUNNING')`, now, now, agentID, agentID); err != nil { return err }
	return tx.Commit()
}

func (s *SQLite) CancelTask(ctx context.Context, id string) (model.Task, error) {
	err := s.finishTask(ctx, id, "CANCELLED", "", "")
	if err != nil && !errors.Is(err, ErrConflict) { return model.Task{}, err }
	task, err := s.GetTask(ctx, id)
	if err != nil { return model.Task{}, err }
	if task.State != model.TaskCancelled { return model.Task{}, ErrConflict }
	return task, nil
}

func (s *SQLite) SetAgentState(ctx context.Context, agentID string, state model.AgentState) error {
	res, err := s.db.ExecContext(ctx, "UPDATE agents SET state=?,updated_at=? WHERE id=?", state, timestamp(), agentID)
	if err != nil { return err }
	n, err := res.RowsAffected()
	if err != nil { return err }
	if n == 0 { return ErrNotFound }
	return nil
}

func (s *SQLite) IngestFeishuEvent(ctx context.Context, eventID, openID, chatID, message string, messageID ...string) (model.Task, bool, error) {
	var replyID string
	if len(messageID) != 0 {
		replyID = messageID[0]
	}
	return s.IngestFeishuAppEvent(ctx, FeishuAppScope{Tenant: "feishu:p2p"}, eventID, openID, chatID, message, replyID)
}

func (s *SQLite) IngestFeishuAppEvent(ctx context.Context, scope FeishuAppScope, eventID, openID, chatID, message, messageID string) (model.Task, bool, error) {
	if eventID == "" || openID == "" || message == "" { return model.Task{}, false, errors.New("event_id, open_id and message are required") }
	identity, inboxID := openID, eventID
	if scope.AppID != "" {
		identity = scopedFeishuKey(scope, openID)
		inboxID = scopedFeishuKey(scope, eventID)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { return model.Task{}, false, err }
	defer tx.Rollback()
	var existingTaskID string
	err = tx.QueryRowContext(ctx, "SELECT task_id FROM event_inbox WHERE event_id=?", inboxID).Scan(&existingTaskID)
	if err == nil {
		task, scanErr := scanTask(tx.QueryRowContext(ctx, `SELECT id,agent_id,request_id,state,payload,result,error_code,created_at,updated_at FROM tasks WHERE id=?`, existingTaskID))
		return task, true, scanErr
	}
	if !errors.Is(err, sql.ErrNoRows) { return model.Task{}, false, err }
	if scope.PendingLimit > 0 {
		var pending int
		err = tx.QueryRowContext(ctx, `SELECT count(*) FROM tasks t JOIN agents a ON a.id=t.agent_id
WHERE a.tenant_id=? AND t.state IN ('QUEUED','RUNNING')`, scope.Tenant).Scan(&pending)
		if err != nil { return model.Task{}, false, err }
		if pending >= scope.PendingLimit { return model.Task{}, false, ErrTenantQueueFull }
	}
	now, agentID := timestamp(), ""
	err = tx.QueryRowContext(ctx, "SELECT id FROM agents WHERE external_identity=?", identity).Scan(&agentID)
	if errors.Is(err, sql.ErrNoRows) {
		agentID = newID("agt")
		_, err = tx.ExecContext(ctx, `INSERT INTO agents
(id,tenant_id,role,state,memory_id,session_id,workspace_id,generation,created_at,updated_at,last_active_at,external_identity)
VALUES(?,?,'supervisor','IDLE',?,?,?,0,?,?,?,?)`,
			agentID,scope.Tenant,newID("mem"),newID("ses"),newID("wsp"),now,now,now,identity)
		if err == nil {
			_, err = tx.ExecContext(ctx, "INSERT INTO runtime_instances(agent_id,state,generation,created_at,updated_at) VALUES(?,'ABSENT',0,?,?)", agentID, now, now)
		}
	}
	if err != nil { return model.Task{}, false, fmt.Errorf("resolve Feishu Agent: %w", err) }
	fields := map[string]string{"source":"feishu","open_id":openID,"chat_id":chatID,"content":message}
	if messageID != "" {
		fields["message_id"] = messageID
	}
	if scope.AppID != "" {
		fields["feishu_route"] = scope.ReplyKey()
	}
	payload, err := json.Marshal(fields)
	if err != nil { return model.Task{}, false, err }
	taskID := newID("tsk")
	_, err = tx.ExecContext(ctx, `INSERT INTO tasks(id,agent_id,request_id,state,payload,created_at,updated_at)
VALUES(?,?,?,'QUEUED',?,?,?)`, taskID,agentID,eventID,string(payload),now,now)
	if err != nil { return model.Task{}, false, err }
	_, err = tx.ExecContext(ctx, "INSERT INTO event_inbox(event_id,agent_id,task_id,source,received_at) VALUES(?,?,?,'feishu',?)", inboxID,agentID,taskID,now)
	if err != nil { return model.Task{}, false, err }
	task, err := scanTask(tx.QueryRowContext(ctx, `SELECT id,agent_id,request_id,state,payload,result,error_code,created_at,updated_at FROM tasks WHERE id=?`, taskID))
	if err != nil { return model.Task{}, false, err }
	if err = tx.Commit(); err != nil { return model.Task{}, false, err }
	return task, false, nil
}

func scopedFeishuKey(scope FeishuAppScope, value string) string {
	digest := sha256.Sum256([]byte(scope.Tenant + "\x00" + scope.AppID + "\x00" + value))
	return "feishu:" + hex.EncodeToString(digest[:])
}

func (s *SQLite) GetRuntime(ctx context.Context, agentID string) (model.Runtime, error) {
	var r model.Runtime
	var state string
	err := s.db.QueryRowContext(ctx, `SELECT agent_id,runtime_id,generation,state,endpoint,last_error,created_at,updated_at
FROM runtime_instances WHERE agent_id=?`, agentID).Scan(&r.AgentID,&r.ID,&r.Generation,&state,&r.Endpoint,&r.LastError,&r.CreatedAt,&r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		if _, agentErr := s.GetAgent(ctx, agentID); agentErr != nil { return model.Runtime{}, agentErr }
		return model.Runtime{AgentID:agentID,State:model.RuntimeAbsent}, nil
	}
	if err != nil { return model.Runtime{}, err }
	r.State = model.RuntimeState(state)
	return r, nil
}

func (s *SQLite) BeginWake(ctx context.Context, agentID string) (model.Runtime, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { return model.Runtime{}, false, err }
	defer tx.Rollback()
	var agentGeneration uint64
	if err = tx.QueryRowContext(ctx, "SELECT generation FROM agents WHERE id=?", agentID).Scan(&agentGeneration); errors.Is(err, sql.ErrNoRows) {
		return model.Runtime{}, false, ErrNotFound
	} else if err != nil { return model.Runtime{}, false, err }
	var r model.Runtime
	var state string
	err = tx.QueryRowContext(ctx, `SELECT agent_id,runtime_id,generation,state,endpoint,last_error,created_at,updated_at
FROM runtime_instances WHERE agent_id=?`, agentID).Scan(&r.AgentID,&r.ID,&r.Generation,&state,&r.Endpoint,&r.LastError,&r.CreatedAt,&r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		now := timestamp()
		_, err = tx.ExecContext(ctx, "INSERT INTO runtime_instances(agent_id,state,generation,created_at,updated_at) VALUES(?,'ABSENT',0,?,?)", agentID,now,now)
		if err != nil { return model.Runtime{}, false, err }
		r = model.Runtime{AgentID:agentID,State:model.RuntimeAbsent}
	} else if err != nil { return model.Runtime{}, false, err } else { r.State = model.RuntimeState(state) }
	switch r.State {
	case model.RuntimeStarting, model.RuntimeReady, model.RuntimeBusy:
		return r, false, nil
	case model.RuntimeStopping:
		return model.Runtime{}, false, ErrConflict
	case model.RuntimeAbsent, model.RuntimeStopped, model.RuntimeFailed:
	default:
		return model.Runtime{}, false, ErrConflict
	}
	now, generation, runtimeID := timestamp(), agentGeneration+1, newID("rt")
	_, err = tx.ExecContext(ctx, `UPDATE agents SET generation=?,state='WAKING',updated_at=? WHERE id=? AND generation=?`, generation,now,agentID,agentGeneration)
	if err != nil { return model.Runtime{}, false, err }
	_, err = tx.ExecContext(ctx, `UPDATE runtime_instances SET runtime_id=?,generation=?,state='STARTING',endpoint='',last_error='',created_at=?,updated_at=? WHERE agent_id=?`,
		runtimeID,generation,now,now,agentID)
	if err != nil { return model.Runtime{}, false, err }
	if err = tx.Commit(); err != nil { return model.Runtime{}, false, err }
	return model.Runtime{AgentID:agentID,ID:runtimeID,Generation:generation,State:model.RuntimeStarting,CreatedAt:now,UpdatedAt:now}, true, nil
}

func (s *SQLite) MarkRuntimeReady(ctx context.Context, runtime model.Runtime) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { return err }
	defer tx.Rollback()
	now := timestamp()
	res, err := tx.ExecContext(ctx, `UPDATE runtime_instances SET state='READY',last_error='',updated_at=?
WHERE agent_id=? AND runtime_id=? AND generation=? AND state='STARTING'`,
		now,runtime.AgentID,runtime.ID,runtime.Generation)
	if err != nil { return err }
	n, err := res.RowsAffected()
	if err != nil { return err }
	if n != 1 { return ErrConflict }
	agentRes, err := tx.ExecContext(ctx, "UPDATE agents SET state='IDLE',updated_at=? WHERE id=? AND generation=? AND state='WAKING'", now,runtime.AgentID,runtime.Generation)
	if err != nil { return err }
	agentRows, err := agentRes.RowsAffected()
	if err != nil { return err }
	if agentRows != 1 { return ErrConflict }
	return tx.Commit()
}

func (s *SQLite) BeginSleep(ctx context.Context, agentID string) (model.Runtime, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { return model.Runtime{}, false, err }
	defer tx.Rollback()
	var r model.Runtime
	var state string
	err = tx.QueryRowContext(ctx, `SELECT agent_id,runtime_id,generation,state,endpoint,last_error,created_at,updated_at
FROM runtime_instances WHERE agent_id=?`,agentID).Scan(&r.AgentID,&r.ID,&r.Generation,&state,&r.Endpoint,&r.LastError,&r.CreatedAt,&r.UpdatedAt)
	if errors.Is(err,sql.ErrNoRows) { return model.Runtime{},false,ErrNotFound }
	if err != nil { return model.Runtime{},false,err }
	r.State=model.RuntimeState(state)
	if r.State==model.RuntimeStopped || r.State==model.RuntimeAbsent { return r,false,nil }
	if r.State==model.RuntimeStopping { return r,false,nil }
	if r.State!=model.RuntimeReady && r.State!=model.RuntimeBusy { return model.Runtime{},false,ErrConflict }
	var active int
	if err=tx.QueryRowContext(ctx,"SELECT COUNT(*) FROM tasks WHERE agent_id=? AND state IN ('QUEUED','RUNNING')",agentID).Scan(&active);err!=nil{return model.Runtime{},false,err}
	if active>0 { return model.Runtime{},false,ErrConflict }
	now:=timestamp()
	res,err:=tx.ExecContext(ctx,"UPDATE runtime_instances SET state='STOPPING',updated_at=? WHERE agent_id=? AND runtime_id=? AND generation=? AND state IN ('READY','BUSY')",now,agentID,r.ID,r.Generation)
	if err!=nil{return model.Runtime{},false,err}
	n,err:=res.RowsAffected();if err!=nil{return model.Runtime{},false,err};if n!=1{return model.Runtime{},false,ErrConflict}
	agentRes,err:=tx.ExecContext(ctx,"UPDATE agents SET state='SLEEP_PENDING',updated_at=? WHERE id=? AND generation=?",now,agentID,r.Generation)
	if err!=nil{return model.Runtime{},false,err}
	agentRows,err:=agentRes.RowsAffected();if err!=nil{return model.Runtime{},false,err};if agentRows!=1{return model.Runtime{},false,ErrConflict}
	if err=tx.Commit();err!=nil{return model.Runtime{},false,err}
	r.State=model.RuntimeStopping;r.UpdatedAt=now
	return r,true,nil
}

func (s *SQLite) MarkRuntimeStopped(ctx context.Context, runtime model.Runtime) error {
	tx,err:=s.db.BeginTx(ctx,nil);if err!=nil{return err};defer tx.Rollback()
	now:=timestamp()
	res,err:=tx.ExecContext(ctx,`UPDATE runtime_instances SET state='STOPPED',endpoint='',updated_at=?
WHERE agent_id=? AND runtime_id=? AND generation=? AND state='STOPPING'`,now,runtime.AgentID,runtime.ID,runtime.Generation)
	if err!=nil{return err};n,err:=res.RowsAffected();if err!=nil{return err};if n!=1{return ErrConflict}
	agentRes,err:=tx.ExecContext(ctx,"UPDATE agents SET state='SLEEPING',updated_at=? WHERE id=? AND generation=?",now,runtime.AgentID,runtime.Generation)
	if err!=nil{return err}
	agentRows,err:=agentRes.RowsAffected();if err!=nil{return err};if agentRows!=1{return ErrConflict}
	return tx.Commit()
}

func (s *SQLite) FailRuntime(ctx context.Context, runtime model.Runtime, reason string) error {
	tx,err:=s.db.BeginTx(ctx,nil);if err!=nil{return err};defer tx.Rollback()
	now:=timestamp()
	res,err:=tx.ExecContext(ctx,`UPDATE runtime_instances SET state='FAILED',last_error=?,updated_at=?
WHERE agent_id=? AND runtime_id=? AND generation=? AND state IN ('STARTING','STOPPING')`,reason,now,runtime.AgentID,runtime.ID,runtime.Generation)
	if err!=nil{return err};n,err:=res.RowsAffected();if err!=nil{return err};if n!=1{return ErrConflict}
	agentRes,err:=tx.ExecContext(ctx,"UPDATE agents SET state='FAILED',updated_at=? WHERE id=? AND generation=?",now,runtime.AgentID,runtime.Generation)
	if err!=nil{return err}
	agentRows,err:=agentRes.RowsAffected();if err!=nil{return err};if agentRows!=1{return ErrConflict}
	return tx.Commit()
}

func (s *SQLite) Close() error { return s.db.Close() }
func timestamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func newID(prefix string) string {
	var raw [16]byte
	if _,err:=rand.Read(raw[:]);err!=nil{return fmt.Sprintf("%s_%d",prefix,time.Now().UnixNano())}
	return prefix+"_"+hex.EncodeToString(raw[:])
}
