package workflow

import (
	"errors"
	"sync"
)

var (
	// ErrRunKeyRequired は Run を識別しない dispatch を拒否したことを表す。
	ErrRunKeyRequired = errors.New("dispatch には Run key が必要である")
	// ErrOperationRequired は実行する関数を持たない dispatch を拒否したことを表す。
	ErrOperationRequired = errors.New("dispatch には実行する Operation が必要である")
	// errFlightAbandoned は Operation が panic して flight が完了しなかったことを表す。
	// Wait している呼び出し側を成功で解放しないための既定値である。
	errFlightAbandoned = errors.New("Operation が結果を残さずに終了した")
)

// Flight は 1 Run に対する state-advancing Operation の in-process 実行である。
// 同じ flight を複数の呼び出し側が Wait でき、結果は全員に同じ値で見える。
type Flight struct {
	done chan struct{}
	err  error
}

// Wait は flight の完了を待ち、Operation の結果を返す。
// 何度でも、いくつの goroutine からでも呼べる。
func (f *Flight) Wait() error {
	<-f.done
	return f.err
}

func settledFlight(err error) *Flight {
	done := make(chan struct{})
	close(done)
	return &Flight{done: done, err: err}
}

// RunDispatcher は Run 単位で state-advancing Operation を単一 flight へ排他する。
//
// 排他の粒度が Run なのは、repository 全体を直列化しないためである
// （docs/spec/05_design/02_workflow.md の Idempotency and recovery）。依存の無い Run は
// 互いの完了を待たずに進む。
//
// 排他は同じ process 内の重複 dispatch にだけ効く。process をまたぐ重複、および
// 外部干渉に対する排他は branch ref create と compare-and-push が担う。ここに
// durable な lock を足すと、GitHub の CAS と二重の排他機構を持つことになる。
type RunDispatcher struct {
	mu      sync.Mutex
	flights map[string]*Flight
}

func NewRunDispatcher() *RunDispatcher {
	return &RunDispatcher{flights: map[string]*Flight{}}
}

// Dispatch は runKey の flight が無ければ operation を開始し、既にあればその flight を
// 返す。join した呼び出し側の operation は実行しない。二重実行を避けるのが目的で
// あり、「後から来た方を実行する」意味論を持たない。
//
// 返り値は必ず非 nil であり、拒否された dispatch も Wait できる完了済み flight として
// 返る。呼び出し側が nil 判定と Wait の両方を書き分けずに済むようにするためである。
func (d *RunDispatcher) Dispatch(runKey string, operation func() error) *Flight {
	if runKey == "" {
		return settledFlight(ErrRunKeyRequired)
	}
	if operation == nil {
		return settledFlight(ErrOperationRequired)
	}

	d.mu.Lock()
	if existing, ok := d.flights[runKey]; ok {
		d.mu.Unlock()
		return existing
	}
	flight := &Flight{done: make(chan struct{})}
	d.flights[runKey] = flight
	d.mu.Unlock()

	go func() {
		// panic しても flight を成功として解放しないよう、既定を失敗にしておく。
		// panic 自体は握り潰さず process へ伝播させる。
		err := errFlightAbandoned
		defer func() {
			d.finish(runKey, flight, err)
		}()
		err = operation()
	}()
	return flight
}

// finish は flight を完了させ、registry から外す。完了済み flight を残すと、
// 同じ Run の次の Operation が永久に join されて進まなくなる。
func (d *RunDispatcher) finish(runKey string, flight *Flight, err error) {
	d.mu.Lock()
	if d.flights[runKey] == flight {
		delete(d.flights, runKey)
	}
	d.mu.Unlock()
	flight.err = err
	close(flight.done)
}
