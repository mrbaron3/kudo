package postgres

import "errors"

var (
	// ErrRunNotFound は指定された ID の Run が存在しないことを表す。
	ErrRunNotFound = errors.New("run not found")
	// ErrVersionConflict は別 writer が expected version を先に更新したことを表す。
	ErrVersionConflict = errors.New("run version conflict")
	// ErrActiveRun は同じ Issue に writer-capable Run が既に存在することを表す。
	ErrActiveRun = errors.New("active writer run already exists for issue")
	// ErrImmutableRunInput は既存 Run の Issue または semantic input の変更を表す。
	ErrImmutableRunInput = errors.New("run issue and input are immutable")
	// ErrInvalidRun は store へ渡された値が永続化 contract を満たさないことを表す。
	ErrInvalidRun = errors.New("invalid run")
	// ErrCorruptRun は保存値が Run の永続化 contract を満たさないことを表す。
	ErrCorruptRun = errors.New("corrupt stored run")
)
