package controller

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func envLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, exists := values[key]
		return value, exists
	}
}

func TestLoadPollConfigAppliesTheDeclaredDefaults(t *testing.T) {
	t.Parallel()

	config, err := LoadPollConfig(envLookup(nil))
	if err != nil {
		t.Fatalf("LoadPollConfig() error = %v", err)
	}
	if !reflect.DeepEqual(config, DefaultPollConfig()) {
		t.Fatalf("config = %#v, want %#v", config, DefaultPollConfig())
	}
	if config.Interval != 15*time.Minute {
		t.Fatalf("既定 interval = %s, want 15m", config.Interval)
	}
}

func TestLoadPollConfigOverridesEachKey(t *testing.T) {
	t.Parallel()

	config, err := LoadPollConfig(envLookup(map[string]string{
		EnvPollInterval:              "3m",
		EnvPollBackoffInitial:        "10s",
		EnvPollBackoffMax:            "2m",
		EnvPollCapacityRetryInterval: "2s",
		EnvTargetAssignee:            "octocat",
		EnvReadyLabel:                "ready-for-ai",
	}))
	if err != nil {
		t.Fatalf("LoadPollConfig() error = %v", err)
	}
	want := PollConfig{
		Interval:              3 * time.Minute,
		BackoffInitial:        10 * time.Second,
		BackoffMax:            2 * time.Minute,
		CapacityRetryInterval: 2 * time.Second,
	}
	want.Filter.Assignee = "octocat"
	want.Filter.ReadyLabel = "ready-for-ai"
	// 再発見の label 条件も同じ ready label から導く。ここがずれると、完了済み Issue への
	// 再依頼を polling が見つけられない。
	overridden := DefaultLabelSet()
	overridden.Ready = "ready-for-ai"
	want.RecoveryQueries = DefaultRecoveryQueries(overridden)
	if !reflect.DeepEqual(config, want) {
		t.Fatalf("config = %#v, want %#v", config, want)
	}
}

// 不正 duration、未対応の値、最低値割れを warning で継続しない
// （docs/spec/05_design/03_runtime-platform.md の Configuration contract）。
func TestLoadPollConfigRejectsInvalidValuesAtStartup(t *testing.T) {
	t.Parallel()

	for name, env := range map[string]map[string]string{
		"単位の無い interval":    {EnvPollInterval: "900"},
		"負の interval":       {EnvPollInterval: "-15m"},
		"ゼロの interval":      {EnvPollInterval: "0"},
		"最低値を下回る interval":  {EnvPollInterval: "30s"},
		"空文字の interval":     {EnvPollInterval: ""},
		"空白だけの interval":    {EnvPollInterval: "   "},
		"backoff の逆転":       {EnvPollBackoffInitial: "5m", EnvPollBackoffMax: "1m"},
		"最低値を下回る backoff":   {EnvPollBackoffInitial: "100ms"},
		"interval を超える再投入":  {EnvPollCapacityRetryInterval: "30m"},
		"空文字の assignee":     {EnvTargetAssignee: ""},
		"空白だけの ready label": {EnvReadyLabel: " "},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if config, err := LoadPollConfig(envLookup(env)); err == nil {
				t.Fatalf("不正な設定を受理した: %#v", config)
			}
		})
	}
}

func TestLoadPollConfigErrorNamesTheOffendingKey(t *testing.T) {
	t.Parallel()

	_, err := LoadPollConfig(envLookup(map[string]string{EnvPollInterval: "900"}))
	if err == nil || !strings.Contains(err.Error(), EnvPollInterval) {
		t.Fatalf("error = %v, want %q を含む", err, EnvPollInterval)
	}
}

func TestLoadTriggerDispatcherConfigBoundsConcurrency(t *testing.T) {
	t.Parallel()

	config, err := LoadTriggerDispatcherConfig(envLookup(nil))
	if err != nil {
		t.Fatalf("LoadTriggerDispatcherConfig() error = %v", err)
	}
	if config.MaxInFlight != DefaultMaxInFlightReconcile {
		t.Fatalf("MaxInFlight = %d, want %d", config.MaxInFlight, DefaultMaxInFlightReconcile)
	}

	config, err = LoadTriggerDispatcherConfig(envLookup(map[string]string{EnvMaxInFlightReconcile: "12"}))
	if err != nil || config.MaxInFlight != 12 {
		t.Fatalf("config = %#v, error = %v", config, err)
	}

	for name, value := range map[string]string{
		"ゼロ":   "0",
		"負":    "-1",
		"非数値":  "many",
		"空文字":  "",
		"上限超え": "10000",
		"小数":   "1.5",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := LoadTriggerDispatcherConfig(envLookup(map[string]string{
				EnvMaxInFlightReconcile: value,
			})); err == nil {
				t.Fatalf("不正な同時実行上限 %q を受理した", value)
			}
		})
	}
}

// 候補を見つける側と人間の trigger を消費する側が別の label を使う状態を作らない。
func TestLoadLabelSetSharesTheReadyLabelWithPolling(t *testing.T) {
	t.Parallel()

	lookup := envLookup(map[string]string{EnvReadyLabel: "ready-for-ai"})
	labels, err := LoadLabelSet(lookup)
	if err != nil {
		t.Fatalf("LoadLabelSet() error = %v", err)
	}
	poll, err := LoadPollConfig(lookup)
	if err != nil {
		t.Fatalf("LoadPollConfig() error = %v", err)
	}
	if labels.Ready != poll.Filter.ReadyLabel {
		t.Fatalf("ready label = %q / %q, want 同じ値", labels.Ready, poll.Filter.ReadyLabel)
	}
	if labels.InProgress != DefaultLabelSet().InProgress {
		t.Fatalf("Kudo 所有 label を上書きした: %#v", labels)
	}
}

func TestLoadLabelSetRejectsAnAmbiguousReadyLabel(t *testing.T) {
	t.Parallel()

	for name, value := range map[string]string{
		"空文字":         "",
		"空白だけ":        " ",
		"Kudo 所有と同じ値": "ai-merged",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := LoadLabelSet(envLookup(map[string]string{EnvReadyLabel: value})); err == nil {
				t.Fatalf("不正な ready label %q を受理した", value)
			}
		})
	}
}
