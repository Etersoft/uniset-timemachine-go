package api

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/pv/uniset-timemachine-go/internal/replay"
)

// Manager отвечает за одну задачу воспроизведения и её управление.
type Manager struct {
	mu sync.Mutex

	service   replay.Service
	sensors   []int64
	defaults  defaults
	job       *job
	jobCancel context.CancelFunc
}

type defaults struct {
	speed     float64
	window    time.Duration
	batchSize int
}

type job struct {
	params      replay.Params
	status      string
	startedAt   time.Time
	finishedAt  time.Time
	stepID      int64
	lastTs      time.Time
	updatesSent int64
	err         error
	commands    chan replay.Command
}

// NewManager создаёт менеджер с заданным сервисом и списком датчиков.
func NewManager(service replay.Service, sensors []int64, speed float64, window time.Duration, batchSize int) *Manager {
	return &Manager{
		service: service,
		sensors: sensors,
		defaults: defaults{
			speed:     speed,
			window:    window,
			batchSize: batchSize,
		},
	}
}

// Start запускает новую задачу. Разрешён только один одновременный запуск.
func (m *Manager) Start(_ context.Context, from, to time.Time, step time.Duration, speed float64, window time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.job != nil && (m.job.status == "running" || m.job.status == "paused" || m.job.status == "stopping") {
		return fmt.Errorf("job is already active")
	}

	if speed <= 0 {
		speed = m.defaults.speed
		if speed <= 0 {
			speed = 1
		}
	}
	if window <= 0 {
		window = m.defaults.window
		if window <= 0 {
			window = 5 * time.Minute
		}
	}

	ctrlCh := make(chan replay.Command, 16)
	params := replay.Params{
		Sensors:   append([]int64(nil), m.sensors...),
		From:      from,
		To:        to,
		Step:      step,
		Window:    window,
		Speed:     speed,
		BatchSize: m.defaults.batchSize,
	}

	// Держим задачу на фоновом контексте, чтобы она не завершалась сразу после ответа HTTP-хендлера.
	jobCtx, cancel := context.WithCancel(context.Background())
	m.jobCancel = cancel
	j := &job{
		params:    params,
		status:    "running",
		startedAt: time.Now(),
		commands:  ctrlCh,
	}
	m.job = j

	go func() {
		err := m.service.RunWithControl(jobCtx, params, replay.Control{
			Commands: ctrlCh,
			OnStep: func(info replay.StepInfo) {
				m.mu.Lock()
				defer m.mu.Unlock()
				if m.job == nil {
					return
				}
				m.job.stepID = info.StepID
				m.job.lastTs = info.StepTs
				m.job.updatesSent += int64(info.UpdatesCount)
			},
		})
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.job != nil {
			m.job.finishedAt = time.Now()
			switch {
			case errors.Is(err, replay.ErrStopped{}):
				m.job.status = "done"
			case err != nil:
				m.job.status = "failed"
				m.job.err = err
			default:
				m.job.status = "done"
				m.job.err = nil
			}
		}
	}()
	return nil
}

// Pause ставит задачу на паузу.
func (m *Manager) Pause() error {
	if err := m.sendCommand(replay.Command{Type: replay.CommandPause}); err != nil {
		return err
	}
	m.setStatus("paused")
	return nil
}

// Resume возобновляет задачу.
func (m *Manager) Resume() error {
	if err := m.sendCommand(replay.Command{Type: replay.CommandResume}); err != nil {
		return err
	}
	m.setStatus("running")
	return nil
}

// Stop останавливает задачу.
func (m *Manager) Stop() error {
	m.mu.Lock()
	if m.job == nil || (m.job.status != "running" && m.job.status != "paused") {
		m.mu.Unlock()
		return fmt.Errorf("no active job")
	}
	m.job.status = "stopping"
	m.mu.Unlock()
	return m.sendCommand(replay.Command{Type: replay.CommandStop})
}

// StepForward выполняет один шаг вперёд из паузы.
func (m *Manager) StepForward() error {
	return m.sendCommand(replay.Command{Type: replay.CommandStepForward})
}

// StepBackward выполняет один шаг назад из паузы (без промежуточных отправок).
func (m *Manager) StepBackward(apply bool) error {
	if err := m.sendCommand(replay.Command{Type: replay.CommandStepBackward, Apply: apply}); err != nil {
		return err
	}
	m.setStatus("paused")
	return nil
}

// Seek перематывает к конкретному моменту. apply=true отправляет финальное состояние в SM.
func (m *Manager) Seek(ts time.Time, apply bool) error {
	if err := m.sendCommand(replay.Command{Type: replay.CommandSeek, TS: ts, Apply: apply}); err != nil {
		return err
	}
	m.setStatus("paused")
	return nil
}

// Apply отправляет текущее состояние в SM одним шагом.
func (m *Manager) Apply() error { return m.sendCommand(replay.Command{Type: replay.CommandApply}) }

// Status возвращает текущие метаданные задачи.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job == nil {
		return Status{Status: "idle"}
	}
	st := Status{
		Status:      m.job.status,
		Params:      m.job.params,
		StartedAt:   m.job.startedAt,
		FinishedAt:  m.job.finishedAt,
		StepID:      m.job.stepID,
		LastTS:      m.job.lastTs,
		UpdatesSent: m.job.updatesSent,
	}
	if m.job.err != nil {
		st.Error = m.job.err.Error()
	}
	return st
}

// State возвращает краткий срез состояния (без значений датчиков).
func (m *Manager) State() StateMeta {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job == nil {
		return StateMeta{}
	}
	return StateMeta{
		StepID:      m.job.stepID,
		LastTS:      m.job.lastTs,
		UpdatesSent: m.job.updatesSent,
		Status:      m.job.status,
	}
}

// Snapshot рассчитывает состояние на момент ts, без отправки в SM.
func (m *Manager) Snapshot(ctx context.Context, ts time.Time) (replay.StateSnapshot, error) {
	params := replay.Params{
		Sensors: m.sensors,
		From:    ts,
		To:      ts,
		Step:    time.Second,
		Window:  m.defaults.window,
	}
	return replay.BuildState(ctx, m.service.Storage, params, ts)
}

type Status struct {
	Status      string        `json:"status"`
	Params      replay.Params `json:"params"`
	StartedAt   time.Time     `json:"started_at"`
	FinishedAt  time.Time     `json:"finished_at"`
	StepID      int64         `json:"step_id"`
	LastTS      time.Time     `json:"last_ts"`
	UpdatesSent int64         `json:"updates_sent"`
	Error       string        `json:"error,omitempty"`
}

type StateMeta struct {
	Status      string    `json:"status"`
	StepID      int64     `json:"step_id"`
	LastTS      time.Time `json:"last_ts"`
	UpdatesSent int64     `json:"updates_sent"`
}

func (m *Manager) sendCommand(cmd replay.Command) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job == nil {
		return fmt.Errorf("no active job")
	}
	if m.job.status == "done" || m.job.status == "failed" {
		return fmt.Errorf("job is already finished")
	}
	if m.job.commands == nil {
		return fmt.Errorf("job is not controllable")
	}
	resp := make(chan error, 1)
	cmd.Resp = resp
	select {
	case m.job.commands <- cmd:
	default:
		return fmt.Errorf("failed to enqueue command")
	}
	select {
	case err := <-resp:
		return err
	case <-time.After(5 * time.Second):
		return fmt.Errorf("command timeout")
	}
}

func (m *Manager) setStatus(status string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job != nil {
		m.job.status = status
	}
}
