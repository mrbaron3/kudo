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
// 返す。started は operation を実際に開始したかである。
//
// join した呼び出し側の operation は実行しない。二重実行を避けるのが目的であり、
// 「後から来た方を実行する」意味論を持たない。started を返すのは、join した flight の
// 結果が別の Operation の結果だからである。started が false の呼び出し側は、その結果を
// 自分の Operation の結果として扱ってはならない。stateless reconciler では、進行中の
// Operation が record surface を進めた後に再観測すれば次の action が導出されるため、
// join 側は待つか、そのまま戻って次の reconcile に委ねるのが正しい。
//
// 返り値の flight は必ず非 nil であり、拒否された dispatch も Wait できる完了済み flight
// として返る。呼び出し側が nil 判定と Wait の両方を書き分けずに済むようにするためである。
func (d *RunDispatcher) Dispatch(runKey string, operation func() error) (flight *Flight, started bool) {
	if runKey == "" {
		return settledFlight(ErrRunKeyRequired), false
	}
	if operation == nil {
		return settledFlight(ErrOperationRequired), false
	}

	d.mu.Lock()
	if existing, ok := d.flights[runKey]; ok {
		d.mu.Unlock()
		return existing, false
	}
	flight = &Flight{done: make(chan struct{})}
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
	return flight, true
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
