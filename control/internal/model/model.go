package model

type AgentState string

const (
	AgentCreated AgentState = "CREATED"
	AgentIdle AgentState = "IDLE"
	AgentWaking AgentState = "WAKING"
	AgentActive AgentState = "ACTIVE"
	AgentSleepPending AgentState = "SLEEP_PENDING"
	AgentSleeping AgentState = "SLEEPING"
	AgentStopping AgentState = "STOPPING"
	AgentStopped AgentState = "STOPPED"
	AgentFailed AgentState = "FAILED"
	AgentDestroyed AgentState = "DESTROYED"
)

type RuntimeState string

const (
	RuntimeAbsent RuntimeState = "ABSENT"
	RuntimeStarting RuntimeState = "STARTING"
	RuntimeReady RuntimeState = "READY"
	RuntimeBusy RuntimeState = "BUSY"
	RuntimeStopping RuntimeState = "STOPPING"
	RuntimeStopped RuntimeState = "STOPPED"
	RuntimeFailed RuntimeState = "FAILED"
)

type TaskState string

const (
	TaskQueued TaskState = "QUEUED"
	TaskRunning TaskState = "RUNNING"
	TaskCompleted TaskState = "COMPLETED"
	TaskCancelled TaskState = "CANCELLED"
	TaskFailed TaskState = "FAILED"
)

type Agent struct {
	ID string `json:"agent_id"`
	TenantID string `json:"tenant_id"`
	Role string `json:"role"`
	State AgentState `json:"state"`
	MemoryID string `json:"memory_id"`
	SessionID string `json:"session_id"`
	WorkspaceID string `json:"workspace_id"`
	Generation uint64 `json:"generation"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	LastActiveAt string `json:"last_active_at"`
}

type Runtime struct {
	AgentID string `json:"agent_id"`
	ID string `json:"runtime_id,omitempty"`
	Generation uint64 `json:"generation"`
	State RuntimeState `json:"runtime_state"`
	Endpoint string `json:"endpoint,omitempty"`
	LastError string `json:"last_error,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type Task struct {
	ID string `json:"task_id"`
	AgentID string `json:"agent_id"`
	RequestID string `json:"request_id"`
	State TaskState `json:"state"`
	Payload string `json:"payload"`
	Result string `json:"result,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type MessageTurn struct {
	User      string `json:"user"`
	Assistant string `json:"assistant"`
}
