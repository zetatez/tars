// Package a2a 实现 A2A (Agent2Agent) 协议 v1.0 的 JSON-RPC 绑定，
// 将 tars 会话暴露为可被其他 agent 发现和调用的远程 agent。
//
// 概念映射：contextId ↔ tars session id；Task ↔ 一次 prompt turn；
// 任务历史 ↔ session message 表。
package a2a

import (
	"encoding/json"
	"time"
)

// TaskState 枚举（ProtoJSON SCREAMING_SNAKE_CASE）。
const (
	StateUnspecified   = "TASK_STATE_UNSPECIFIED"
	StateSubmitted     = "TASK_STATE_SUBMITTED"
	StateWorking       = "TASK_STATE_WORKING"
	StateCompleted     = "TASK_STATE_COMPLETED"
	StateFailed        = "TASK_STATE_FAILED"
	StateCanceled      = "TASK_STATE_CANCELED"
	StateInputRequired = "TASK_STATE_INPUT_REQUIRED"
	StateRejected      = "TASK_STATE_REJECTED"
	StateAuthRequired  = "TASK_STATE_AUTH_REQUIRED"
)

// Role 枚举。
const (
	RoleUser  = "ROLE_USER"
	RoleAgent = "ROLE_AGENT"
)

// JSON-RPC / A2A 错误码。
const (
	CodeParseError           = -32700
	CodeInvalidRequest       = -32600
	CodeMethodNotFound       = -32601
	CodeInvalidParams        = -32602
	CodeInternalError        = -32603
	CodeTaskNotFound         = -32001
	CodeTaskNotCancelable    = -32002
	CodePushNotSupported     = -32003
	CodeUnsupportedOp        = -32004
	CodeContentTypeNotSupp   = -32005
	CodeInvalidAgentResp     = -32006
	CodeExtCardNotConfigured = -32007
	CodeVersionNotSupported  = -32009
)

// ProtocolVersion 是本实现支持的 A2A 协议版本。
const ProtocolVersion = "1.0"

// Part 是消息/产物的最小内容单元，text/raw/url/data 四选一。
type Part struct {
	Text      *string        `json:"text,omitempty"`
	Raw       []byte         `json:"raw,omitempty"`
	URL       string         `json:"url,omitempty"`
	Data      any            `json:"data,omitempty"`
	Filename  string         `json:"filename,omitempty"`
	MediaType string         `json:"mediaType,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// TextPart 构造文本 part。
func TextPart(s string) Part {
	return Part{Text: &s}
}

// Message 是客户端与 agent 间的一次通信单元。
type Message struct {
	Role      string         `json:"role"`
	MessageID string         `json:"messageId"`
	ContextID string         `json:"contextId,omitempty"`
	TaskID    string         `json:"taskId,omitempty"`
	Parts     []Part         `json:"parts"`
	Metadata  map[string]any `json:"metadata,omitempty"`

	// extension 只保留 text 内容，其余 part 类型在解析时丢弃/报错。
}

// TaskStatus 是任务当前状态。
type TaskStatus struct {
	State     string   `json:"state"`
	Message   *Message `json:"message,omitempty"`
	Timestamp string   `json:"timestamp,omitempty"`
}

// Task 是 A2A 的核心工作单元。
type Task struct {
	ID        string         `json:"id"`
	ContextID string         `json:"contextId,omitempty"`
	Status    TaskStatus     `json:"status"`
	Artifacts []Artifact     `json:"artifacts,omitempty"`
	History   []Message      `json:"history,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// Artifact 是任务的产物输出。
type Artifact struct {
	ArtifactID  string         `json:"artifactId"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Parts       []Part         `json:"parts"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	LastChunk   bool           `json:"-"`
}

// StatusUpdateEvent 通知任务状态变更。
type StatusUpdateEvent struct {
	TaskID    string         `json:"taskId"`
	ContextID string         `json:"contextId"`
	Status    TaskStatus     `json:"status"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// ArtifactUpdateEvent 通知产物生成/更新。
type ArtifactUpdateEvent struct {
	TaskID    string   `json:"taskId"`
	ContextID string   `json:"contextId"`
	Artifact  Artifact `json:"artifact"`
	Append    bool     `json:"append,omitempty"`
	LastChunk bool     `json:"lastChunk,omitempty"`
}

// StreamResponse 包装流式事件，task/message/statusUpdate/artifactUpdate 四选一。
type StreamResponse struct {
	Task           *Task                `json:"task,omitempty"`
	Message        *Message             `json:"message,omitempty"`
	StatusUpdate   *StatusUpdateEvent   `json:"statusUpdate,omitempty"`
	ArtifactUpdate *ArtifactUpdateEvent `json:"artifactUpdate,omitempty"`
}

// nowTimestamp 返回规格要求的 ISO 8601 UTC 毫秒时间戳。
func nowTimestamp() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

// terminalState 判断是否终态。
func terminalState(state string) bool {
	switch state {
	case StateCompleted, StateFailed, StateCanceled, StateRejected:
		return true
	}
	return false
}

// extractText 从 parts 中提取全部 text 内容，返回拼接文本与是否存在非文本 part。
func extractText(parts []Part) (string, bool) {
	var texts []string
	nonText := false
	for _, p := range parts {
		if p.Text != nil {
			texts = append(texts, *p.Text)
			continue
		}
		nonText = true
	}
	out := ""
	for i, t := range texts {
		if i > 0 {
			out += "\n"
		}
		out += t
	}
	return out, nonText
}

// contentText 解析 tars message.content JSON 中的 text 字段。
func contentText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var c struct {
		Text  string `json:"text"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(content, &c); err != nil {
		return string(content)
	}
	if c.Error != "" {
		return c.Error
	}
	return c.Text
}
