package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

var (
	ErrTaskNotFound = errors.New("plugin task not found")
	ErrTaskConflict = errors.New("plugin task state conflict")
)

const _redisOperationTimeout = 3 * time.Second

type TaskStatus string

const (
	TaskRunning   TaskStatus = "RUNNING"
	TaskPaused    TaskStatus = "PAUSED"
	TaskCompleted TaskStatus = "COMPLETED"
	TaskFailed    TaskStatus = "FAILED"
	TaskCancelled TaskStatus = "CANCELLED"
)

type Task struct {
	ID             string                   `json:"id"`
	TenantCode     string                   `json:"tenantCode"`
	UserID         string                   `json:"userId"`
	WorkflowID     string                   `json:"workflowId"`
	Inputs         map[string]any           `json:"inputs"`
	Status         TaskStatus               `json:"status"`
	NextSequence   int                      `json:"nextSequence"`
	CurrentCommand *AgentCommand            `json:"currentCommand,omitempty"`
	CurrentMemory  string                   `json:"currentMemory,omitempty"`
	CurrentLeads   []Lead                   `json:"currentLeads,omitempty"`
	ResultSummary  string                   `json:"resultSummary,omitempty"`
	Leads          []Lead                   `json:"leads,omitempty"`
	Results        map[string]CommandResult `json:"results"`
	LastResult     *CommandResult           `json:"lastResult,omitempty"`
	Observation    *Observation             `json:"observation,omitempty"`
	Observations   map[string]Observation   `json:"observations,omitempty"`
	Steps          []AgentStep              `json:"steps,omitempty"`
	Events         []AgentEvent             `json:"events,omitempty"`
	Planning       bool                     `json:"planning,omitempty"`
	PlanningAt     time.Time                `json:"planningAt,omitempty"`
	CreatedAt      time.Time                `json:"createdAt"`
	UpdatedAt      time.Time                `json:"updatedAt"`
}

type AgentStep struct {
	Command AgentCommand  `json:"command"`
	Result  CommandResult `json:"result"`
	Memory  string        `json:"memory,omitempty"`
	Leads   []Lead        `json:"leads,omitempty"`
}

type AgentEvent struct {
	Type   string    `json:"type"`
	PageID string    `json:"pageId,omitempty"`
	At     time.Time `json:"at"`
}

type Store interface {
	Create(task Task) error
	Update(
		tenantCode string,
		userID string,
		taskID string,
		update func(task *Task) error,
	) error
}

type MemoryStore struct {
	mu    sync.Mutex
	tasks map[string]*Task
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{tasks: make(map[string]*Task)}
}

func (s *MemoryStore) Create(task Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tasks[task.ID]; exists {
		return ErrTaskConflict
	}
	s.tasks[task.ID] = &task
	return nil
}

func (s *MemoryStore) Update(
	tenantCode string,
	userID string,
	taskID string,
	update func(task *Task) error,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[taskID]
	if !ok || task.TenantCode != tenantCode || (task.UserID != "" && task.UserID != userID) {
		return ErrTaskNotFound
	}
	if err := update(task); err != nil {
		return err
	}
	task.UpdatedAt = time.Now().UTC()
	return nil
}

type RedisStore struct {
	client *redis.Client
	ttl    time.Duration
}

func NewRedisStore(client *redis.Client, ttl time.Duration) *RedisStore {
	return &RedisStore{client: client, ttl: ttl}
}

func (s *RedisStore) Create(task Task) error {
	data, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal plugin task: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), _redisOperationTimeout)
	defer cancel()
	created, err := s.client.SetNX(
		ctx, redisTaskKey(task.TenantCode, task.ID), data, s.ttl,
	).Result()
	if err != nil {
		return fmt.Errorf("store plugin task: %w", err)
	}
	if !created {
		return ErrTaskConflict
	}
	return nil
}

func (s *RedisStore) Update(
	tenantCode string,
	userID string,
	taskID string,
	update func(task *Task) error,
) error {
	ctx, cancel := context.WithTimeout(context.Background(), _redisOperationTimeout)
	defer cancel()
	key := redisTaskKey(tenantCode, taskID)
	const maxAttempts = 5
	for attempt := 0; attempt < maxAttempts; attempt++ {
		err := s.client.Watch(ctx, func(transaction *redis.Tx) error {
			data, err := transaction.Get(ctx, key).Bytes()
			if errors.Is(err, redis.Nil) {
				return ErrTaskNotFound
			}
			if err != nil {
				return fmt.Errorf("load plugin task: %w", err)
			}
			var task Task
			if err := json.Unmarshal(data, &task); err != nil {
				return fmt.Errorf("decode plugin task: %w", err)
			}
			if task.TenantCode != tenantCode || (task.UserID != "" && task.UserID != userID) {
				return ErrTaskNotFound
			}
			if err := update(&task); err != nil {
				return err
			}
			task.UpdatedAt = time.Now().UTC()
			updated, err := json.Marshal(task)
			if err != nil {
				return fmt.Errorf("marshal plugin task: %w", err)
			}
			_, err = transaction.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, key, updated, s.ttl)
				return nil
			})
			return err
		}, key)
		if !errors.Is(err, redis.TxFailedErr) {
			return err
		}
	}
	return fmt.Errorf("update plugin task after %d attempts: %w", maxAttempts, ErrTaskConflict)
}

func redisTaskKey(tenantCode string, taskID string) string {
	return "tenant:" + tenantCode + ":plugin:task:" + taskID
}
