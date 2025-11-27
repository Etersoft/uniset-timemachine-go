package replay

import (
	"time"

	"github.com/pv/uniset-timemachine-go/internal/sharedmem"
)

// CommandType задаёт тип управляющей команды.
type CommandType int

const (
	CommandPause CommandType = iota + 1
	CommandResume
	CommandStop
	CommandStepForward
	CommandStepBackward
	CommandSeek
	CommandApply
	CommandSaveOutput
)

// Command передаёт управляющее сообщение в RunWithControl.
type Command struct {
	Type       CommandType
	TS         time.Time
	Apply      bool
	SaveOutput bool
	Resp       chan<- error
}

// Control объединяет каналы управления и коллбеки прогресса.
type Control struct {
	Commands  <-chan Command
	OnStep    func(StepInfo)
	OnUpdates func(StepInfo, []sharedmem.SensorUpdate)
}

// StepInfo описывает прогресс шага при управляемом проигрывании.
type StepInfo struct {
	StepID       int64
	StepTs       time.Time
	UpdatesCount int
}

// ErrStopped возвращается при остановке через команду Stop.
type ErrStopped struct{}

func (ErrStopped) Error() string { return "stopped" }
