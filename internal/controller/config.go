package controller

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Controller が読む environment key。正本は
// docs/spec/05_design/03_runtime-platform.md の Configuration contract である。
const (
	EnvPollInterval              = "KUDO_POLL_INTERVAL"
	EnvPollBackoffInitial        = "KUDO_POLL_BACKOFF_INITIAL"
	EnvPollBackoffMax            = "KUDO_POLL_BACKOFF_MAX"
	EnvPollCapacityRetryInterval = "KUDO_POLL_CAPACITY_RETRY_INTERVAL"
	EnvTargetAssignee            = "KUDO_TARGET_ASSIGNEE"
	EnvReadyLabel                = "KUDO_READY_LABEL"
	EnvMaxInFlightReconcile      = "KUDO_MAX_INFLIGHT_RECONCILE"
)

// 同時に走らせる ReconcileIssue の上限。KUDO_MAX_CONCURRENCY（同時 Operation 上限）とは
// 別軸であり、こちらは観測と導出を含む reconcile の受付側 capacity である。
const (
	DefaultMaxInFlightReconcile = 4
	MaxMaxInFlightReconcile     = 1024
)

// Lookup は environment 変数の参照である。os.LookupEnv をそのまま渡せる。
//
// 関数として受け取るのは、設定の読み取りを process 全体の状態から切り離し、
// deterministic に検証できるようにするためである。
type Lookup func(key string) (string, bool)

// LoadPollConfig は polling の設定を environment から読み、起動時に strict validation する。
//
// 未設定の key だけが既定値になる。設定されているが空、単位が無い、最低値を下回る値は
// warning で継続せずに拒否する。「設定し忘れ」と「明示的に既定を選んだ」を同じ結果に
// すると、間違った値のまま起動した deployment が観測できなくなる
// （docs/spec/05_design/03_runtime-platform.md の Configuration contract）。
func LoadPollConfig(lookup Lookup) (PollConfig, error) {
	if lookup == nil {
		return PollConfig{}, fmt.Errorf("environment lookup は必須")
	}
	config := DefaultPollConfig()
	durations := []struct {
		key    string
		target *time.Duration
	}{
		{EnvPollInterval, &config.Interval},
		{EnvPollBackoffInitial, &config.BackoffInitial},
		{EnvPollBackoffMax, &config.BackoffMax},
		{EnvPollCapacityRetryInterval, &config.CapacityRetryInterval},
	}
	for _, entry := range durations {
		value, err := lookupDuration(lookup, entry.key, *entry.target)
		if err != nil {
			return PollConfig{}, err
		}
		*entry.target = value
	}
	assignee, err := lookupString(lookup, EnvTargetAssignee, config.Filter.Assignee)
	if err != nil {
		return PollConfig{}, err
	}
	readyLabel, err := lookupString(lookup, EnvReadyLabel, config.Filter.ReadyLabel)
	if err != nil {
		return PollConfig{}, err
	}
	config.Filter.Assignee = assignee
	config.Filter.ReadyLabel = readyLabel
	labels, err := LoadLabelSet(lookup)
	if err != nil {
		return PollConfig{}, err
	}
	// 再発見の label 条件を同じ loader から導くのは、候補を見つける側と記録する側で
	// ready label がずれないようにするためである。
	config.RecoveryQueries = DefaultRecoveryQueries(labels)
	if err := config.Validate(); err != nil {
		return PollConfig{}, fmt.Errorf("polling 設定が不正: %w", err)
	}
	return config, nil
}

// LoadLabelSet は記録側の label 名を environment から読む。
//
// LoadPollConfig と同じ KUDO_READY_LABEL を読むのは、候補を見つける側（poller）と
// 人間の trigger を消費する側（recorder）が別の label を使う状態を作らないためである。
// 値を composition root で書き写すと、書き忘れが「候補は毎 cycle 発見されるが
// `ai-ready`は永久に消費されない」という無言の停止になる。
//
// Kudo 所有の 3 label（in-progress / needs-human / merged）は上書き対象ではない。
// 人間が付ける trigger と違い、これらは Kudo の記録であり、deployment ごとに変える
// 理由が無い（docs/spec/05_design/04_github-routing.md の Labels）。
func LoadLabelSet(lookup Lookup) (LabelSet, error) {
	if lookup == nil {
		return LabelSet{}, fmt.Errorf("environment lookup は必須")
	}
	labels := DefaultLabelSet()
	ready, err := lookupString(lookup, EnvReadyLabel, labels.Ready)
	if err != nil {
		return LabelSet{}, err
	}
	labels.Ready = ready
	if err := labels.Validate(); err != nil {
		return LabelSet{}, fmt.Errorf("label 設定が不正: %w", err)
	}
	return labels, nil
}

// LoadTriggerDispatcherConfig は reconcile の同時実行上限を environment から読む。
func LoadTriggerDispatcherConfig(lookup Lookup) (TriggerDispatcherConfig, error) {
	if lookup == nil {
		return TriggerDispatcherConfig{}, fmt.Errorf("environment lookup は必須")
	}
	raw, exists := lookup(EnvMaxInFlightReconcile)
	if !exists {
		return TriggerDispatcherConfig{MaxInFlight: DefaultMaxInFlightReconcile}, nil
	}
	maxInFlight, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return TriggerDispatcherConfig{}, fmt.Errorf("%s が整数ではない: %q", EnvMaxInFlightReconcile, raw)
	}
	if maxInFlight < 1 || maxInFlight > MaxMaxInFlightReconcile {
		return TriggerDispatcherConfig{}, fmt.Errorf("%s は 1〜%d でなければならない: %d",
			EnvMaxInFlightReconcile, MaxMaxInFlightReconcile, maxInFlight)
	}
	return TriggerDispatcherConfig{MaxInFlight: maxInFlight}, nil
}

func lookupDuration(lookup Lookup, key string, fallback time.Duration) (time.Duration, error) {
	raw, exists := lookup(key)
	if !exists {
		return fallback, nil
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, fmt.Errorf("%s が空である", key)
	}
	value, err := time.ParseDuration(trimmed)
	if err != nil {
		// 単位の無い値は Go 自身が拒否する（例外は`0`だけ）。ここで error を作り直すのは、
		// どの key が不正かを名指しするためである。ParseDuration の message は値しか
		// 含まないので、複数 key を読む起動時検証では原因を特定できない。
		return 0, fmt.Errorf("%s が duration ではない（単位が必要）: %q", key, raw)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s は正の duration でなければならない: %q", key, raw)
	}
	return value, nil
}

func lookupString(lookup Lookup, key, fallback string) (string, error) {
	raw, exists := lookup(key)
	if !exists {
		return fallback, nil
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%s が空である", key)
	}
	return trimmed, nil
}
