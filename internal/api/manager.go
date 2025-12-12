package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/pv/uniset-timemachine-go/internal/replay"
	"github.com/pv/uniset-timemachine-go/internal/sharedmem"
	"github.com/pv/uniset-timemachine-go/internal/storage"
	"github.com/pv/uniset-timemachine-go/pkg/config"
)

var (
	errControlLocked   = errors.New("control is locked by another session")
	errSessionRequired = errors.New("session token is required")
)

// Manager отвечает за одну задачу воспроизведения и её управление.
type Manager struct {
	mu sync.Mutex

	service        replay.Service
	sensors        []int64 // рабочий список hashes (cityhash64(name))
	defaultSensors []int64 // дефолтный список hashes
	defaults       defaults
	job            *job
	jobCancel      context.CancelFunc
	streamer       *StateStreamer
	sensorInfo     map[int64]SensorInfo // hash → SensorInfo
	pending        pendingState
	// Управляющая сессия
	controllerSession  string
	controllerLastSeen time.Time
	controlTimeout     time.Duration
}

type defaults struct {
	speed       float64
	window      time.Duration
	batchSize   int
	saveOutput  bool
	saveAllowed bool
}

type pendingState struct {
	rangeSet bool
	rng      replay.Params
	seekSet  bool
	seekTs   time.Time
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

type SessionStatus struct {
	Session           string `json:"session"`
	IsController      bool   `json:"is_controller"`
	ControllerPresent bool   `json:"controller_present"`
	ControllerSession string `json:"controller_session"`
	ControllerAgeSec  int64  `json:"controller_age_sec"`
	ControlTimeoutSec int64  `json:"control_timeout_sec"`
	CanClaim          bool   `json:"can_claim"`
}

// ControlStatus возвращает наличие контроллера и таймаут (секунды).
func (m *Manager) ControlStatus() (present bool, timeoutSec int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.controllerSession != "", int(m.controlTimeout.Seconds())
}

// NewManager создаёт менеджер с заданным сервисом и списком хешей датчиков.
// sensors содержит hashes (cityhash64(name)).
func NewManager(service replay.Service, sensors []int64, cfg *config.Config, speed float64, window time.Duration, batchSize int, streamer *StateStreamer, saveAllowed bool, defaultSave bool, controlTimeout time.Duration) *Manager {
	metaHashes := sensors
	if cfg != nil && cfg.Registry != nil && cfg.Registry.Count() > 0 {
		metaHashes = cfg.Registry.AllHashesSortedByName()
		// По умолчанию рабочий список — полный словарь из конфига.
		sensors = metaHashes
	}
	defaultSensors := append([]int64(nil), sensors...)
	info := BuildSensorInfo(cfg, metaHashes)
	m := &Manager{
		service:        service,
		sensors:        sensors,
		defaultSensors: defaultSensors,
		defaults: defaults{
			speed:       speed,
			window:      window,
			batchSize:   batchSize,
			saveAllowed: saveAllowed,
			saveOutput:  saveAllowed && defaultSave,
		},
		streamer:           streamer,
		sensorInfo:         info,
		controlTimeout:     controlTimeout,
		controllerLastSeen: time.Time{},
	}
	if m.streamer != nil {
		m.streamer.Reset(info)
	}
	return m
}

// RequireControl гарантирует, что токен принадлежит активной сессии.
// Если контроллер отсутствует, закрепляет токен как контроллера.
func (m *Manager) RequireControl(token string) error {
	if token == "" {
		return errSessionRequired
	}
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.controllerSession == "" {
		m.controllerSession = token
		m.controllerLastSeen = now
		return nil
	}
	if token == m.controllerSession {
		m.controllerLastSeen = now
		return nil
	}
	// Есть другой контроллер.
	if m.controlTimeout > 0 && now.Sub(m.controllerLastSeen) > m.controlTimeout {
		// Истекла, но нужен явный claim.
		return fmt.Errorf("%w (stale; claim required)", errControlLocked)
	}
	return errControlLocked
}

// ClaimControl пытается перехватить управление для токена, если контроллер отсутствует или просрочен.
func (m *Manager) ClaimControl(token string) error {
	if token == "" {
		return errSessionRequired
	}
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.controllerSession == "" || m.controllerSession == token {
		m.controllerSession = token
		m.controllerLastSeen = now
		return nil
	}
	if m.controlTimeout == 0 {
		return errControlLocked
	}
	if now.Sub(m.controllerLastSeen) <= m.controlTimeout {
		return errControlLocked
	}
	m.controllerSession = token
	m.controllerLastSeen = now
	return nil
}

// SessionStatus возвращает информацию о текущем контроллере и возможности захвата.
// Только возвращает статус, не меняет состояние. Для захвата управления используйте ClaimControl().
func (m *Manager) SessionStatus(token string) SessionStatus {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()

	// SessionStatus только возвращает информацию, не меняет состояние.
	// Чтобы стать контроллером, нужно явно вызвать ClaimControl().

	var age int64
	if !m.controllerLastSeen.IsZero() {
		age = int64(now.Sub(m.controllerLastSeen).Seconds())
	}
	isCtrl := token != "" && token == m.controllerSession
	timeoutSec := int64(m.controlTimeout.Seconds())
	canClaim := false
	if m.controlTimeout > 0 {
		if m.controllerSession == "" || now.Sub(m.controllerLastSeen) > m.controlTimeout {
			canClaim = true
		}
	}
	return SessionStatus{
		Session:           token,
		IsController:      isCtrl,
		ControllerPresent: m.controllerSession != "",
		ControllerSession: m.controllerSession,
		ControllerAgeSec:  age,
		ControlTimeoutSec: timeoutSec,
		CanClaim:          canClaim,
	}
}

// KeepAlive обновляет lastSeen для текущего контроллера (не меняя владельца).
func (m *Manager) KeepAlive(token string) error {
	if token == "" {
		return errSessionRequired
	}
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.controllerSession == "" {
		return errControlLocked
	}
	if token != m.controllerSession {
		return errControlLocked
	}
	m.controllerLastSeen = now
	return nil
}

// ReleaseControl освобождает управление. Может вызвать только текущий контроллер.
// При force=true сбрасывает контроллера без проверки токена (служебные сценарии/тесты).
func (m *Manager) ReleaseControl(token string, force bool) error {
	if token == "" && !force {
		return errSessionRequired
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.controllerSession == "" {
		return errControlLocked
	}
	if !force && token != m.controllerSession {
		return errControlLocked
	}
	m.controllerSession = ""
	m.controllerLastSeen = time.Time{}
	return nil
}

// Reset сбрасывает состояние задачи и pending.
func (m *Manager) Reset() {
	m.mu.Lock()
	if m.jobCancel != nil {
		m.jobCancel()
		m.jobCancel = nil
	}
	m.job = nil
	m.pending = pendingState{}
	if len(m.defaultSensors) > 0 {
		m.sensors = append([]int64(nil), m.defaultSensors...)
	}
	m.mu.Unlock()
}

// SensorsInfo возвращает копию метаданных датчиков.
func (m *Manager) SensorsInfo() map[int64]SensorInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	clone := make(map[int64]SensorInfo, len(m.sensorInfo))
	for id, info := range m.sensorInfo {
		clone[id] = info
	}
	return clone
}

// StartPending запускает задачу, используя отложенный диапазон.
func (m *Manager) StartPending(ctx context.Context) error {
	m.mu.Lock()
	hasRange := m.pending.rangeSet
	rng := m.pending.rng
	seekSet := m.pending.seekSet
	seekTs := m.pending.seekTs
	m.mu.Unlock()
	if !hasRange {
		return fmt.Errorf("pending range is not set")
	}
	if err := m.Start(ctx, rng.From, rng.To, rng.Step, rng.Speed, rng.Window, rng.SaveOutput); err != nil {
		return err
	}
	if seekSet {
		if err := m.Seek(seekTs, false); err != nil {
			logDebugf("[manager] pending seek apply failed: %v", err)
		} else {
			// После отложенного seek остаёмся в paused внутри сервиса; нужно возобновить.
			if err := m.Resume(); err != nil {
				logDebugf("[manager] pending seek resume failed: %v", err)
			}
		}
	}
	return nil
}

// SetRange сохраняет диапазон/параметры без старта.
func (m *Manager) SetRange(from, to time.Time, step time.Duration, speed float64, window time.Duration, saveOutput bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	save := m.defaults.saveAllowed && saveOutput
	m.pending.rangeSet = true
	m.pending.rng = replay.Params{
		Sensors:    append([]int64(nil), m.sensors...),
		From:       from,
		To:         to,
		Step:       step,
		Speed:      speed,
		Window:     window,
		BatchSize:  m.defaults.batchSize,
		SaveOutput: save,
	}
}

// SetPendingSeek запоминает желаемый seek.
func (m *Manager) SetPendingSeek(ts time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending.seekSet = true
	m.pending.seekTs = ts
}

// Start запускает новую задачу. Разрешён только один одновременный запуск.
func (m *Manager) Start(_ context.Context, from, to time.Time, step time.Duration, speed float64, window time.Duration, saveOutput bool) error {
	m.mu.Lock()
	if m.job != nil && (m.job.status == "running" || m.job.status == "paused" || m.job.status == "stopping") {
		m.mu.Unlock()
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
	save := m.defaults.saveAllowed && saveOutput

	ctrlCh := make(chan replay.Command, 16)
	params := replay.Params{
		Sensors:    append([]int64(nil), m.sensors...),
		From:       from,
		To:         to,
		Step:       step,
		Window:     window,
		Speed:      speed,
		BatchSize:  m.defaults.batchSize,
		SaveOutput: save,
	}

	var streamReset map[int64]SensorInfo
	streamer := m.streamer
	if streamer != nil {
		streamReset = make(map[int64]SensorInfo, len(m.sensorInfo))
		for id, info := range m.sensorInfo {
			streamReset[id] = info
		}
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
	// очищаем pending после старта
	m.pending = pendingState{}
	m.mu.Unlock()

	if streamer != nil {
		streamer.Reset(streamReset)
	}

	go func() {
		err := m.service.RunWithControl(jobCtx, params, replay.Control{
			Commands: ctrlCh,
			OnStep: func(info replay.StepInfo) {
				logDebugf("[event] step=%d ts=%s updates=%d", info.StepID, info.StepTs.Format(time.RFC3339), info.UpdatesCount)
				m.mu.Lock()
				defer m.mu.Unlock()
				if m.job == nil {
					return
				}
				m.job.stepID = info.StepID
				m.job.lastTs = info.StepTs
				m.job.updatesSent += int64(info.UpdatesCount)
			},
			OnUpdates: func(info replay.StepInfo, updates []sharedmem.SensorUpdate) {
				if m.streamer == nil {
					return
				}
				m.streamer.Publish(info, updates)
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
			// Сохраняем pending диапазон/seek для последующих шагов в idle/done.
			if m.pending.rangeSet == false {
				m.pending.rangeSet = true
				m.pending.rng = m.job.params
			}
			if m.job.lastTs.IsZero() {
				m.pending.seekTs = m.job.params.To
			} else {
				m.pending.seekTs = m.job.lastTs
			}
			m.pending.seekSet = true
		}
		log.Printf("[manager] RunWithControl finished err=%v", err)
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

// SetSaveOutput обновляет флаг сохранения в SM для текущей задачи.
func (m *Manager) SetSaveOutput(save bool) error {
	m.mu.Lock()
	if m.job == nil || m.job.commands == nil {
		m.mu.Unlock()
		return fmt.Errorf("no active job")
	}
	m.job.params.SaveOutput = save
	m.mu.Unlock()
	return m.sendCommand(replay.Command{Type: replay.CommandSaveOutput, SaveOutput: save})
}

// Sensors возвращает метаданные всех известных датчиков (из конфига).
func (m *Manager) Sensors() []SensorInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := make([]SensorInfo, 0, len(m.sensorInfo))
	for _, info := range m.sensorInfo {
		list = append(list, info)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Name == list[j].Name {
			return list[i].Hash < list[j].Hash
		}
		return list[i].Name < list[j].Name
	})
	return list
}

// WorkingSensors возвращает копию текущего рабочего списка хешей датчиков.
func (m *Manager) WorkingSensors() []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]int64, len(m.sensors))
	copy(out, m.sensors)
	return out
}

// SetWorkingSensors устанавливает текущий рабочий список датчиков.
// hashes содержит хеши датчиков (cityhash64(name)).
// Возвращает количество принятых и отклонённых хешей.
func (m *Manager) SetWorkingSensors(hashes []int64) (int, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	seen := make(map[int64]struct{})
	accepted := make([]int64, 0, len(hashes))
	rejected := 0
	for _, hash := range hashes {
		if _, ok := m.sensorInfo[hash]; !ok {
			rejected++
			continue
		}
		if _, dup := seen[hash]; dup {
			continue
		}
		seen[hash] = struct{}{}
		accepted = append(accepted, hash)
	}
	if len(accepted) == 0 {
		return 0, rejected, fmt.Errorf("no valid sensors")
	}
	m.sensors = accepted
	if m.pending.rangeSet {
		m.pending.rng.Sensors = append([]int64(nil), accepted...)
	}
	return len(accepted), rejected, nil
}

// WorkingSensorNames возвращает имена текущих рабочих датчиков.
func (m *Manager) WorkingSensorNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.sensors))
	for _, hash := range m.sensors {
		if info, ok := m.sensorInfo[hash]; ok {
			names = append(names, info.Name)
		}
	}
	return names
}

// SetWorkingSensorsByNames устанавливает рабочий список датчиков по именам.
// Возвращает количество принятых, отклонённых имён и срез отклонённых имён.
func (m *Manager) SetWorkingSensorsByNames(names []string) (int, []string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Построим обратный индекс name → hash
	nameToHash := make(map[string]int64, len(m.sensorInfo))
	for hash, info := range m.sensorInfo {
		nameToHash[info.Name] = hash
	}

	seen := make(map[int64]struct{})
	accepted := make([]int64, 0, len(names))
	rejected := make([]string, 0)
	for _, name := range names {
		hash, ok := nameToHash[name]
		if !ok {
			rejected = append(rejected, name)
			continue
		}
		if _, dup := seen[hash]; dup {
			continue
		}
		seen[hash] = struct{}{}
		accepted = append(accepted, hash)
	}
	if len(accepted) == 0 {
		return 0, rejected, fmt.Errorf("no valid sensors")
	}
	m.sensors = accepted
	if m.pending.rangeSet {
		m.pending.rng.Sensors = append([]int64(nil), accepted...)
	}
	return len(accepted), rejected, nil
}

// Stop останавливает задачу.
func (m *Manager) Stop() error {
	m.mu.Lock()
	if m.job == nil {
		m.mu.Unlock()
		return nil
	}
	// Сохраняем текущий диапазон и позицию, чтобы при следующем старте продолжить с последнего места.
	m.pending.rangeSet = true
	m.pending.rng = m.job.params
	if !m.job.lastTs.IsZero() {
		m.pending.seekSet = true
		m.pending.seekTs = m.job.lastTs
	}
	// Если уже остановились, просто выходим без ошибок.
	if m.job.status == "done" || m.job.status == "failed" {
		m.mu.Unlock()
		return nil
	}
	// Если уже в процессе остановки и работа фактически завершена, переводим в done.
	if m.job.status == "stopping" {
		if !m.job.finishedAt.IsZero() {
			m.job.status = "done"
		}
		m.mu.Unlock()
		return nil
	}
	m.job.status = "stopping"
	m.mu.Unlock()
	if err := m.sendCommand(replay.Command{Type: replay.CommandStop}); err != nil {
		if errors.Is(err, replay.ErrStopped{}) {
			return nil
		}
		return err
	}
	return nil
}

// StepForward выполняет один шаг вперёд из паузы.
func (m *Manager) StepForward() error {
	if handled := m.stepPendingWithoutJob(true); handled {
		return nil
	}
	if err := m.sendCommand(replay.Command{Type: replay.CommandStepForward}); err != nil {
		return err
	}
	// После единичного шага остаёмся в paused, чтобы пользователь мог двигаться дальше вручную.
	m.setStatus("paused")
	return nil
}

// StepBackward выполняет один шаг назад из паузы (без промежуточных отправок).
func (m *Manager) StepBackward(apply bool) error {
	if handled := m.stepPendingWithoutJob(false); handled {
		return nil
	}
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
	m.mu.Lock()
	prevStatus := ""
	if m.job != nil {
		m.job.lastTs = ts
		prevStatus = m.job.status
	}
	m.mu.Unlock()
	if prevStatus != "running" {
		m.setStatus("paused")
	}
	return nil
}

// Apply отправляет текущее состояние в SM одним шагом.
func (m *Manager) Apply() error { return m.sendCommand(replay.Command{Type: replay.CommandApply}) }

// Status возвращает текущие метаданные задачи.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job == nil {
		pending := m.pendingStateLocked()
		st := "idle"
		if pending.RangeSet {
			st = "pending"
		}
		return Status{Status: st, Pending: pending, SaveAllowed: m.defaults.saveAllowed}
	}
	st := Status{
		Status:      m.job.status,
		Params:      m.job.params,
		StartedAt:   m.job.startedAt,
		FinishedAt:  m.job.finishedAt,
		StepID:      m.job.stepID,
		LastTS:      m.job.lastTs,
		UpdatesSent: m.job.updatesSent,
		Pending:     m.pendingStateLocked(),
		SaveAllowed: m.defaults.saveAllowed,
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

func (m *Manager) pendingStateLocked() Pending {
	return Pending{
		RangeSet: m.pending.rangeSet,
		Range:    m.pending.rng,
		SeekSet:  m.pending.seekSet,
		SeekTS:   m.pending.seekTs,
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

// stepPendingWithoutJob двигает pending.seekTs, если задачи нет (idle/done) и задан диапазон.
func (m *Manager) stepPendingWithoutJob(forward bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stepPendingLocked(forward)
}

// stepPendingLocked двигает pending.seekTs, ожидая, что m.mu уже удержан.
func (m *Manager) stepPendingLocked(forward bool) bool {
	if m.job != nil && m.job.status != "done" && m.job.status != "failed" {
		return false
	}
	if !m.pending.rangeSet {
		return false
	}
	step := m.pending.rng.Step
	if step == 0 {
		step = time.Second
	}
	cur := m.pending.seekTs
	if cur.IsZero() {
		cur = m.pending.rng.From
	}
	var next time.Time
	if forward {
		next = cur.Add(step)
		if next.After(m.pending.rng.To) {
			next = m.pending.rng.To
		}
	} else {
		next = cur.Add(-step)
		if next.Before(m.pending.rng.From) {
			next = m.pending.rng.From
		}
	}
	m.pending.seekSet = true
	m.pending.seekTs = next
	// статус оставляем как есть (обычно idle/done), чтобы UI видел pending seek.
	return true
}

// Range возвращает минимальный/максимальный timestamp для текущего списка датчиков.
func (m *Manager) Range(ctx context.Context) (time.Time, time.Time, int64, error) {
	min, max, count, _, err := m.RangeWithUnknown(ctx)
	return min, max, count, err
}

// RangeWithUnknown возвращает диапазон, количество известных датчиков и число неизвестных
// (датчиков, которых нет в конфиге, но они есть в окне истории).
// Unknown будет 0, если хранилище не поддерживает UnknownAwareStorage.
func (m *Manager) RangeWithUnknown(ctx context.Context) (time.Time, time.Time, int64, int64, error) {
	// Доступный диапазон считаем по всему объёму истории, без учёта текущего pending-диапазона,
	// чтобы кнопка «установить доступный диапазон» всегда возвращала реальные границы данных.
	return m.RangeWithUnknownBounds(ctx, time.Time{}, time.Time{})
}

// RangeWithUnknownBounds считает unknown в указанном окне [from,to]. Если не поддерживается — unknown=0.
func (m *Manager) RangeWithUnknownBounds(ctx context.Context, from, to time.Time) (time.Time, time.Time, int64, int64, error) {
	if ua, ok := m.service.Storage.(storage.UnknownAwareStorage); ok {
		return ua.RangeWithUnknown(ctx, m.sensors, from, to)
	}
	min, max, count, err := m.service.Storage.Range(ctx, m.sensors, from, to)
	return min, max, count, 0, err
}

func (m *Manager) SensorsCount(ctx context.Context, from, to time.Time) (int64, error) {
	_, _, count, err := m.service.Storage.Range(ctx, m.sensors, from, to)
	return count, err
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
	Pending     Pending       `json:"pending,omitempty"`
	SaveAllowed bool          `json:"save_allowed"`
}

type StateMeta struct {
	Status      string    `json:"status"`
	StepID      int64     `json:"step_id"`
	LastTS      time.Time `json:"last_ts"`
	UpdatesSent int64     `json:"updates_sent"`
}

// Pending описывает отложенные параметры диапазона/seek.
type Pending struct {
	RangeSet bool          `json:"range_set"`
	Range    replay.Params `json:"range"`
	SeekSet  bool          `json:"seek_set"`
	SeekTS   time.Time     `json:"seek_ts"`
}

func (m *Manager) sendCommand(cmd replay.Command) error {
	m.mu.Lock()
	isStep := cmd.Type == replay.CommandStepForward || cmd.Type == replay.CommandStepBackward
	if m.job == nil || m.job.status == "done" || m.job.status == "failed" || m.job.commands == nil {
		if isStep {
			forward := cmd.Type == replay.CommandStepForward
			handled := m.stepPendingLocked(forward)
			if handled {
				m.mu.Unlock()
				return nil
			}
		}
		m.mu.Unlock()
		if m.job == nil {
			return fmt.Errorf("no active job")
		}
		if m.job.status == "done" || m.job.status == "failed" {
			return fmt.Errorf("job is already finished")
		}
		return fmt.Errorf("job is not controllable")
	}
	resp := make(chan error, 1)
	cmd.Resp = resp
	tsStr := ""
	if !cmd.TS.IsZero() {
		tsStr = cmd.TS.Format(time.RFC3339)
	}
	logDebugf("[command] send %v apply=%t ts=%s", cmd.Type, cmd.Apply, tsStr)
	select {
	case m.job.commands <- cmd:
	default:
		m.mu.Unlock()
		return fmt.Errorf("failed to enqueue command")
	}
	m.mu.Unlock()
	select {
	case err := <-resp:
		logDebugf("[command] result %v err=%v", cmd.Type, err)
		return err
	case <-time.After(30 * time.Second):
		logDebugf("[command] timeout %v", cmd.Type)
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

// PendingState возвращает копию отложенных параметров.
func (m *Manager) PendingState() Pending {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pendingStateLocked()
}
