package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrRecorderIdentityRequired = errors.New("recorder identity が設定されていない")

// EnsureComment は同じ marker を持つ既存 comment を全 page から検索し、
// 見つからない場合だけ actor identity で新しい comment を作る。
func (g *Gateway) EnsureComment(ctx context.Context, targetNumber int64, record CommentRecord) (Comment, bool, error) {
	if targetNumber <= 0 {
		return Comment{}, false, fmt.Errorf("comment target number は正数でなければならない")
	}
	if err := g.validateMarkerRepository(record.Marker); err != nil {
		return Comment{}, false, err
	}
	if g.recorder == nil {
		return Comment{}, false, ErrRecorderIdentityRequired
	}
	rendered, err := RenderComment(record.Body, record.Marker, record.MachineBlock)
	if err != nil {
		return Comment{}, false, err
	}
	matches, err := g.findMarkedComments(ctx, targetNumber, record.Marker)
	if err != nil {
		return Comment{}, false, err
	}
	if len(matches) > 1 {
		return Comment{}, false, invalidResponse("search comments", "同じ marker の comment が複数ある", nil)
	}
	if len(matches) == 1 {
		return matches[0], false, nil
	}
	created, err := g.createComment(ctx, targetNumber, rendered)
	if err != nil {
		return Comment{}, false, err
	}
	return created, true, nil
}

// findMarkedComments は recorder identity 名義で同じ marker を持つ comment を全 page から
// 集める。複数一致をここで失敗にしないのは、許容できるかが record の種類で違うためである。
// content digest を marker に持つ記録では複数一致は protocol 違反だが、identity marker を
// 持つ収束対象の comment では収束を続けられなければならない。
func (g *Gateway) findMarkedComments(ctx context.Context, targetNumber int64, marker Marker) ([]Comment, error) {
	comments, err := g.listComments(ctx, targetNumber)
	if err != nil {
		return nil, err
	}
	var matches []Comment
	for _, comment := range comments {
		if comment.Author.ID != g.recorder.CommentAuthor.ID {
			continue
		}
		markers, parseErr := ParseMarkers(string(comment.Body))
		if parseErr != nil {
			return nil, invalidResponse("search comments", "既存 comment の marker が不正", parseErr)
		}
		if len(markers) > 1 {
			return nil, invalidResponse("search comments", "既存 comment に marker が複数ある", nil)
		}
		for _, candidate := range markers {
			if markersEqual(candidate, marker) {
				matches = append(matches, comment)
				break
			}
		}
	}
	return matches, nil
}

func (g *Gateway) createComment(ctx context.Context, targetNumber int64, body string) (Comment, error) {
	response, err := g.request(ctx, http.MethodPost, g.endpoint(g.issuePath(targetNumber)+"/comments", nil),
		struct {
			Body string `json:"body"`
		}{Body: body}, http.StatusCreated)
	if err != nil {
		return Comment{}, err
	}
	var created apiComment
	if err := json.Unmarshal(response.Body, &created); err != nil {
		return Comment{}, invalidResponse("POST comment", "created comment response を decode できない", err)
	}
	comment, err := convertComment(created)
	if err != nil {
		return Comment{}, invalidResponse("POST comment", "created comment body が不正", err)
	}
	if comment.Author.ID != g.recorder.CommentAuthor.ID {
		return Comment{}, &TransportFailure{
			Class:     FailurePermission,
			Operation: "POST comment",
			Message:   "created comment の author が configured recorder identity と一致しない",
		}
	}
	return comment, nil
}

// EnsureCheckRun は head 上の同名 check run を全 page から検索し、同じ marker が
// 存在しない場合だけ completed check run を作る。
func (g *Gateway) EnsureCheckRun(ctx context.Context, record CheckRunRecord) (CheckRun, bool, error) {
	if err := g.validateCheckRunRecord(record); err != nil {
		return CheckRun{}, false, err
	}
	if g.recorder == nil {
		return CheckRun{}, false, ErrRecorderIdentityRequired
	}
	text, err := RenderCheckRunText(record.Marker, record.MachineBlock)
	if err != nil {
		return CheckRun{}, false, err
	}
	existing, err := g.listCheckRuns(ctx, record.HeadSHA, record.Name)
	if err != nil {
		return CheckRun{}, false, err
	}
	var matches []CheckRun
	for _, checkRun := range existing {
		if checkRun.App.ID != g.recorder.CheckRunApp.ID {
			continue
		}
		markers, parseErr := ParseMarkers(checkRun.Output.Text + "\n" + checkRun.Output.Summary)
		if parseErr != nil {
			return CheckRun{}, false, invalidResponse("search check runs", "既存 check run の marker が不正", parseErr)
		}
		if len(markers) > 1 {
			return CheckRun{}, false, invalidResponse("search check runs", "既存 check run に marker が複数ある", nil)
		}
		for _, marker := range markers {
			if markersEqual(marker, record.Marker) {
				matches = append(matches, checkRun)
				break
			}
		}
	}
	if len(matches) > 1 {
		return CheckRun{}, false, invalidResponse("search check runs", "同じ marker の check run が複数ある", nil)
	}
	if len(matches) == 1 {
		return matches[0], false, nil
	}

	type output struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
		Text    string `json:"text"`
	}
	type createCheckRun struct {
		Name       string `json:"name"`
		HeadSHA    string `json:"head_sha"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		DetailsURL string `json:"details_url,omitempty"`
		Output     output `json:"output"`
	}
	response, err := g.request(ctx, http.MethodPost, g.endpoint(g.repositoryPath("check-runs"), nil), createCheckRun{
		Name:       record.Name,
		HeadSHA:    record.HeadSHA,
		Status:     "completed",
		Conclusion: record.Conclusion,
		DetailsURL: record.DetailsURL,
		Output:     output{Title: record.Title, Summary: record.Summary, Text: text},
	}, http.StatusCreated)
	if err != nil {
		return CheckRun{}, false, err
	}
	var created apiCheckRun
	if err := json.Unmarshal(response.Body, &created); err != nil {
		return CheckRun{}, false, invalidResponse("POST check run", "created check run response を decode できない", err)
	}
	checkRun := convertCheckRun(created)
	if checkRun.App.ID != g.recorder.CheckRunApp.ID {
		return CheckRun{}, false, &TransportFailure{
			Class:     FailurePermission,
			Operation: "POST check run",
			Message:   "created check run の App が configured recorder identity と一致しない",
		}
	}
	return checkRun, true, nil
}

// EnsureLabel は current label set の全 page を確認し、label name が無い場合だけ追加する。
// GitHub label は marker を格納できないため、case-insensitive な label name 自体が収束 key になる。
func (g *Gateway) EnsureLabel(ctx context.Context, issueNumber int64, label string) (bool, error) {
	if issueNumber <= 0 || !validLabelName(label) {
		return false, fmt.Errorf("Issue number または label name が不正")
	}
	labels, err := g.listLabels(ctx, issueNumber)
	if err != nil {
		return false, err
	}
	for _, existing := range labels {
		if strings.EqualFold(existing.Name, label) {
			return false, nil
		}
	}
	_, err = g.request(ctx, http.MethodPost, g.endpoint(g.issuePath(issueNumber)+"/labels", nil),
		struct {
			Labels []string `json:"labels"`
		}{Labels: []string{label}}, http.StatusOK)
	if err != nil {
		return false, err
	}
	return true, nil
}

// ConvergeLabels は current label set を一度だけ読み、add が無ければ付け、
// remove にある label が付いていれば外す。
//
// 一覧の読み取りを 1 回にまとめているのは、label 単位の primitive を並べると
// 同じ Issue の label を 3〜4 回読み直すためである。polling は rate limit 予算の
// 制約下にある回復経路であり、収束済みの Run を毎 cycle 記録するこの経路の
// 読み取りは定常的な支出になる。
//
// 削除は観測した label name をそのまま使う。GitHub の label identity は
// case-insensitive だが、DELETE の path は表記が一致しないと対象を解決できず、
// 設定値で消しにいくと `AI-Ready` が付いた Issue で 404 になり、外れていないのに
// 収束済みとして報告される。
//
// 戻り値は「この呼び出しが変えたか」である。既に収束している状態は成功であり、
// 失敗ではない。add と remove に同じ label を渡す呼び出しは、収束先が定義できないため
// 拒否する。
func (g *Gateway) ConvergeLabels(ctx context.Context, issueNumber int64, add string, remove []string) (bool, []string, error) {
	if issueNumber <= 0 || !validLabelName(add) {
		return false, nil, fmt.Errorf("Issue number または label name が不正")
	}
	for _, label := range remove {
		if !validLabelName(label) {
			return false, nil, fmt.Errorf("Issue number または label name が不正")
		}
		if strings.EqualFold(label, add) {
			return false, nil, fmt.Errorf("label %q を同じ呼び出しで追加と削除の両方に指定できない", add)
		}
	}
	labels, err := g.listLabels(ctx, issueNumber)
	if err != nil {
		return false, nil, err
	}
	present := make(map[string]string, len(labels))
	for _, existing := range labels {
		present[strings.ToLower(existing.Name)] = existing.Name
	}

	added := false
	if _, exists := present[strings.ToLower(add)]; !exists {
		if _, err := g.request(ctx, http.MethodPost, g.endpoint(g.issuePath(issueNumber)+"/labels", nil),
			struct {
				Labels []string `json:"labels"`
			}{Labels: []string{add}}, http.StatusOK); err != nil {
			return false, nil, err
		}
		added = true
	}

	var removed []string
	for _, label := range remove {
		observed, exists := present[strings.ToLower(label)]
		if !exists {
			continue
		}
		changed, err := g.deleteLabel(ctx, issueNumber, observed)
		if err != nil {
			return added, removed, err
		}
		if changed {
			removed = append(removed, observed)
		}
	}
	return added, removed, nil
}

// deleteLabel は観測済みの label name を外す。
//
// 404 を失敗にしないのは、同じ収束を並行 reconcile が先に済ませた状態が transport
// failure として記録されると retry が終わらないためである。観測した name をそのまま
// 使っているため、404 は「既に外れていた」以外の意味を持たない。
func (g *Gateway) deleteLabel(ctx context.Context, issueNumber int64, label string) (bool, error) {
	response, err := g.request(ctx, http.MethodDelete,
		g.endpoint(g.issuePath(issueNumber)+"/labels/"+url.PathEscape(label), nil), nil,
		http.StatusOK, http.StatusNotFound)
	if err != nil {
		return false, err
	}
	return response.Status == http.StatusOK, nil
}

// EnsureIssueClosed は open な Issue だけを close する。
//
// merge completion の投影は label と close の両方であり、片方だけ成功した状態から
// 次の reconcile が再開できなければならない。現在値を確認してから mutate するのは、
// 人間が close した Issue を Kudo の完了として上書きしないためでもある。
// 戻り値は「この呼び出しが close したか」である。
func (g *Gateway) EnsureIssueClosed(ctx context.Context, issueNumber int64) (bool, error) {
	if issueNumber <= 0 {
		return false, fmt.Errorf("Issue number は正数でなければならない")
	}
	observed, err := g.getIssue(ctx, issueNumber)
	if err != nil {
		return false, err
	}
	if strings.EqualFold(observed.Issue.State, "closed") {
		return false, nil
	}
	_, err = g.request(ctx, http.MethodPatch, g.endpoint(g.issuePath(issueNumber), nil), struct {
		State       string `json:"state"`
		StateReason string `json:"state_reason"`
	}{State: "closed", StateReason: "completed"}, http.StatusOK)
	if err != nil {
		return false, err
	}
	return true, nil
}

// oldestComment は同じ marker を持つ comment 群から record の実体を選ぶ。
//
// 複数一致は、marker の確認と作成の間に並行 reconcile が割り込んだ痕跡である
// （単一 process でも webhook と poll が同じ Issue を同時に投入し得る）。ここを失敗に
// すると、以後その記録は更新も収束もできない。最古の 1 件を実体として扱えば収束は
// 続く。重複の掃除は人間が行い、構造的な解決は reconcile core の Issue 単位
// single-flight が担う。
func oldestComment(matches []Comment) (Comment, bool) {
	if len(matches) == 0 {
		return Comment{}, false
	}
	oldest := matches[0]
	for _, comment := range matches[1:] {
		if comment.ID < oldest.ID {
			oldest = comment
		}
	}
	return oldest, true
}

// CommentChange は EnsureCommentContent が行った mutation の分類である。
type CommentChange string

const (
	CommentUnchanged CommentChange = "unchanged"
	CommentCreated   CommentChange = "created"
	CommentUpdated   CommentChange = "updated"
)

// EnsureCommentContent は marker で既存 comment を特定し、本文が違う場合だけ更新する。
//
// EnsureComment との違いは、marker が一致する comment の本文を現在値へ収束させる点である。
// 内容が変わり得る案内 comment では、本文の digest を marker へ入れて EnsureComment を
// 使うと、文面を変えるたびに別 marker の comment が増える。marker を record の identity
// （kind と対象）だけで固定し、本文の収束をこちらで行うことで重複を作らない。
//
// 戻り値は収束後の comment と、この呼び出しが行った mutation の分類である。
func (g *Gateway) EnsureCommentContent(ctx context.Context, targetNumber int64, record CommentRecord) (Comment, CommentChange, error) {
	if targetNumber <= 0 {
		return Comment{}, CommentUnchanged, fmt.Errorf("comment target number は正数でなければならない")
	}
	if err := g.validateMarkerRepository(record.Marker); err != nil {
		return Comment{}, CommentUnchanged, err
	}
	if g.recorder == nil {
		return Comment{}, CommentUnchanged, ErrRecorderIdentityRequired
	}
	rendered, err := RenderComment(record.Body, record.Marker, record.MachineBlock)
	if err != nil {
		return Comment{}, CommentUnchanged, err
	}
	matches, err := g.findMarkedComments(ctx, targetNumber, record.Marker)
	if err != nil {
		return Comment{}, CommentUnchanged, err
	}
	if existing, found := oldestComment(matches); found {
		if string(existing.Body) == rendered {
			return existing, CommentUnchanged, nil
		}
		updated, updateErr := g.updateComment(ctx, existing.ID, rendered)
		if updateErr != nil {
			return Comment{}, CommentUnchanged, updateErr
		}
		return updated, CommentUpdated, nil
	}
	created, err := g.createComment(ctx, targetNumber, rendered)
	if err != nil {
		return Comment{}, CommentUnchanged, err
	}
	return created, CommentCreated, nil
}

func (g *Gateway) updateComment(ctx context.Context, commentID int64, body string) (Comment, error) {
	response, err := g.request(ctx, http.MethodPatch,
		g.endpoint(g.repositoryPath("issues/comments")+"/"+strconv.FormatInt(commentID, 10), nil),
		struct {
			Body string `json:"body"`
		}{Body: body}, http.StatusOK)
	if err != nil {
		return Comment{}, err
	}
	var updated apiComment
	if err := json.Unmarshal(response.Body, &updated); err != nil {
		return Comment{}, invalidResponse("PATCH comment", "updated comment response を decode できない", err)
	}
	comment, err := convertComment(updated)
	if err != nil {
		return Comment{}, invalidResponse("PATCH comment", "updated comment body が不正", err)
	}
	if comment.Author.ID != g.recorder.CommentAuthor.ID {
		return Comment{}, &TransportFailure{
			Class:     FailurePermission,
			Operation: "PATCH comment",
			Message:   "updated comment の author が configured recorder identity と一致しない",
		}
	}
	return comment, nil
}

func (g *Gateway) listLabels(ctx context.Context, issueNumber int64) ([]Label, error) {
	seen := make(map[string]struct{})
	var result []Label
	err := g.paginate(ctx, g.issuePath(issueNumber)+"/labels", nil, func(data []byte) error {
		var page []apiLabel
		if err := json.Unmarshal(data, &page); err != nil {
			return err
		}
		for _, value := range page {
			key := strings.ToLower(value.Name)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, Label{Name: value.Name, Color: value.Color})
		}
		return nil
	})
	return result, err
}

func (g *Gateway) validateMarkerRepository(marker Marker) error {
	if err := validateMarker(marker); err != nil {
		return err
	}
	if marker.Repository.canonical() != g.repository {
		return fmt.Errorf("marker repository %s は gateway repository %s と一致しない",
			marker.Repository.String(), g.repository.String())
	}
	return nil
}

func (g *Gateway) validateCheckRunRecord(record CheckRunRecord) error {
	if err := g.validateMarkerRepository(record.Marker); err != nil {
		return err
	}
	if record.Marker.Head != record.HeadSHA || !shaPattern.MatchString(record.HeadSHA) {
		return fmt.Errorf("check run head と marker head が一致しない")
	}
	if !strings.HasPrefix(record.Name, "kudo/") || !recordNamePattern.MatchString(record.Name) {
		return fmt.Errorf("check run name は kudo/ namespace の正規名でなければならない")
	}
	switch record.Conclusion {
	case "action_required", "cancelled", "failure", "neutral", "success", "skipped", "stale", "timed_out":
	default:
		return fmt.Errorf("check run conclusion が不正: %q", record.Conclusion)
	}
	if record.Title == "" || record.Summary == "" || !utf8.ValidString(record.Title) || !utf8.ValidString(record.Summary) {
		return fmt.Errorf("check run title と summary は UTF-8 の非空文字列でなければならない")
	}
	if strings.Contains(record.Summary, markerPrefix) || strings.Contains(record.Summary, machinePrefix) {
		return fmt.Errorf("check run summary に予約済み record prefix を含められない")
	}
	if len(record.Title) > 1024 || len(record.Summary) > MaxRecordSurfaceBytes {
		return ErrRecordSurfaceTooLarge
	}
	if record.DetailsURL != "" {
		parsed, err := url.Parse(record.DetailsURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("check run details URL が不正")
		}
	}
	return nil
}

func markersEqual(left, right Marker) bool {
	left.Repository = left.Repository.canonical()
	right.Repository = right.Repository.canonical()
	return left == right
}

func validLabelName(label string) bool {
	return label != "" && len(label) <= 50 && utf8.ValidString(label) &&
		!strings.ContainsFunc(label, unicode.IsControl)
}
