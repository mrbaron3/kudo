package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// EnsureComment は同じ marker を持つ既存 comment を全 page から検索し、
// 見つからない場合だけ actor identity で新しい comment を作る。
func (g *Gateway) EnsureComment(ctx context.Context, targetNumber int64, record CommentRecord) (Comment, bool, error) {
	if targetNumber <= 0 {
		return Comment{}, false, fmt.Errorf("comment target number は正数でなければならない")
	}
	if err := g.validateMarkerRepository(record.Marker); err != nil {
		return Comment{}, false, err
	}
	rendered, err := RenderComment(record.Body, record.Marker, record.MachineBlock)
	if err != nil {
		return Comment{}, false, err
	}
	comments, err := g.listComments(ctx, targetNumber)
	if err != nil {
		return Comment{}, false, err
	}
	var matches []Comment
	for _, comment := range comments {
		markers, parseErr := ParseMarkers(string(comment.Body))
		if parseErr != nil {
			return Comment{}, false, invalidResponse("search comments", "既存 comment の marker が不正", parseErr)
		}
		if len(markers) > 1 {
			return Comment{}, false, invalidResponse("search comments", "既存 comment に marker が複数ある", nil)
		}
		for _, marker := range markers {
			if markersEqual(marker, record.Marker) {
				matches = append(matches, comment)
				break
			}
		}
	}
	if len(matches) > 1 {
		return Comment{}, false, invalidResponse("search comments", "同じ marker の comment が複数ある", nil)
	}
	if len(matches) == 1 {
		return matches[0], false, nil
	}

	response, err := g.request(ctx, http.MethodPost, g.endpoint(g.issuePath(targetNumber)+"/comments", nil),
		struct {
			Body string `json:"body"`
		}{Body: rendered}, http.StatusCreated)
	if err != nil {
		return Comment{}, false, err
	}
	var created apiComment
	if err := json.Unmarshal(response.Body, &created); err != nil {
		return Comment{}, false, invalidResponse("POST comment", "created comment response を decode できない", err)
	}
	comment, err := convertComment(created)
	if err != nil {
		return Comment{}, false, invalidResponse("POST comment", "created comment body が不正", err)
	}
	return comment, true, nil
}

// EnsureCheckRun は head 上の同名 check run を全 page から検索し、同じ marker が
// 存在しない場合だけ completed check run を作る。
func (g *Gateway) EnsureCheckRun(ctx context.Context, record CheckRunRecord) (CheckRun, bool, error) {
	if err := g.validateCheckRunRecord(record); err != nil {
		return CheckRun{}, false, err
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
	return convertCheckRun(created), true, nil
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
