package reviewagent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mrbaron3/kudo/internal/agentpackage"
	"github.com/mrbaron3/kudo/internal/contract"
)

// Provider は Review Agent runtime が必要とする fresh session 境界である。
// Codex/Claude の選択と CLI 固有 flag は実装側へ閉じ込める。
type Provider interface {
	Run(
		context.Context,
		contract.WorkerExecutionPolicy,
		agentpackage.Package,
		[]byte,
		string,
	) ([]byte, error)
}

type Clock interface {
	Now() time.Time
}

type Executor struct {
	Provider Provider
	Clock    Clock
}

// ExecuteTestValidity は package input 構築、fresh provider execution、strict output binding を
// 一回の Review Agent attempt として接続する。retry と report は呼び出し側の Review Worker が所有する。
func (e Executor) ExecuteTestValidity(
	ctx context.Context,
	prepared PreparedReview,
	executionPolicy contract.ExecutionPolicy,
	workingDirectory string,
	reviewRunID string,
) (contract.ReviewResult, error) {
	if e.Provider == nil || e.Clock == nil {
		return contract.ReviewResult{}, errors.New("review Agent provider と clock は必須")
	}
	policyRef, _, err := contract.EncodeExecutionPolicy(executionPolicy)
	if err != nil {
		return contract.ReviewResult{}, err
	}
	if policyRef != prepared.Request.ExecutionPolicy {
		return contract.ReviewResult{}, fmt.Errorf("Execution Policy が Review Request と一致しない")
	}
	request, err := BuildTestValidityRequest(prepared)
	if err != nil {
		return contract.ReviewResult{}, err
	}
	output, err := e.Provider.Run(
		ctx,
		executionPolicy.ReviewWorker,
		prepared.Package,
		append([]byte(nil), request...),
		workingDirectory,
	)
	if err != nil {
		return contract.ReviewResult{}, err
	}
	return BindTestValidityOutput(prepared, request, output, reviewRunID, e.Clock.Now())
}
