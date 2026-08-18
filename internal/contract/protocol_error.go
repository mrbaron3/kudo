package contract

import (
	"errors"
	"fmt"
)

// ProtocolCode は Worker Operation protocol と Implementation–Review protocol の
// validation 失敗の機械可読な分類である。
//
// 分類は docs/contracts/operation-protocol-v1alpha1.md の Validation 節が列挙する
// 拒否理由に対応する。値は安定した識別子であり、変更は versioned protocol の変更として扱う。
//
// Issue Contract parser の Code とは別の値空間である。parser の error は Issue body の
// 行・section・field を指す text 由来の診断であり、protocol validation は解決済みの
// 値に対する判定で行番号を持たない。同じ型へ寄せると、どちらの利用者にも意味のない
// field を持つ error になる。
type ProtocolCode string

const (
	// ProtocolSchemaUnknown は envelope 自身の schema が既知 version でないことを表す。
	ProtocolSchemaUnknown ProtocolCode = "protocol_schema_unknown"
	// ProtocolKindUnknown は Operation kind または Review kind が語彙に無いことを表す。
	ProtocolKindUnknown ProtocolCode = "protocol_kind_unknown"
	// ProtocolFieldMissing は required field が空であることを表す。
	ProtocolFieldMissing ProtocolCode = "protocol_field_missing"
	// ProtocolFieldInvalid は field の値が形式規則を満たさないことを表す。
	ProtocolFieldInvalid ProtocolCode = "protocol_field_invalid"
	// ProtocolFieldDuplicate は集合として扱う field に重複があることを表す。
	ProtocolFieldDuplicate ProtocolCode = "protocol_field_duplicate"
	// ProtocolFieldTooLong は canonical text の上限を超えたことを表す。
	ProtocolFieldTooLong ProtocolCode = "protocol_field_too_long"
	// ProtocolKindConstraint は kind に許されない field combination を表す。
	// 個々の field は妥当でも、その kind では持てない・省略できない場合に使う。
	ProtocolKindConstraint ProtocolCode = "protocol_kind_constraint"
	// ProtocolIdentityMismatch は再計算した digest と参照された identity の不一致を表す。
	ProtocolIdentityMismatch ProtocolCode = "protocol_identity_mismatch"
	// ProtocolOutcomeConflict は結末の排他規則違反を表す。
	// quality verdict と attempt failure を同時に持つ、verdict と finding が矛盾する等。
	ProtocolOutcomeConflict ProtocolCode = "protocol_outcome_conflict"
)

// Error は ProtocolCode 自身を sentinel error として使えるようにする。これにより
// 呼び出し側は errors.Is(err, ProtocolKindUnknown) で分岐でき、error 文字列に依存しない。
func (c ProtocolCode) Error() string { return string(c) }

// ProtocolError は protocol validation の失敗 1 件である。
//
// transport failure を含まない。timeout や provider crash を validation error と
// 同じ型で運ぶと、immutable な入力に対する permanent な契約違反を retry 可能な失敗として
// 扱う経路ができる。transport failure は AttemptFailure が表す。
type ProtocolError struct {
	Code    ProtocolCode
	Field   string // 違反した field の protocol 上の名前。envelope 全体に関わる場合は空
	Message string
}

// Error は人間向けの説明を返す。機械判定には Code を使う。
func (e *ProtocolError) Error() string {
	if e.Field == "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%s [%s]: %s", e.Code, e.Field, e.Message)
}

// Unwrap は Code を返し、errors.Is による分類を成立させる。
func (e *ProtocolError) Unwrap() error { return e.Code }

// ProtocolViolation は err が protocol validation の失敗かを判定して内容を返す。
//
// true を返す err は immutable な入力に対する permanent な契約違反であり、同じ入力で
// retry してはならない。判定できない error を retry 可能側の既定へ倒すと、契約違反を
// 無限に retry しうるため、本 package が返す error 以外は false になる。
func ProtocolViolation(err error) (*ProtocolError, bool) {
	var target *ProtocolError
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

func protocolErr(code ProtocolCode, field, format string, args ...any) error {
	return &ProtocolError{Code: code, Field: field, Message: fmt.Sprintf(format, args...)}
}
