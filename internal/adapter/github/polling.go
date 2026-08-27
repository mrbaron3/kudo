package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/workflow"
)

// RateLimitSnapshot は直近の GitHub response が報告した rate limit の残量である。
//
// 「今どれだけ残っているか」を別 endpoint へ問い合わせないのは、確認自体が
// 予算を消費し、確認と利用の間で値が古くなるためである。値は最後に観測した
// response の時点のものであり、他の in-flight request の消費を含まない。
// 観測時刻を持たないのは、gateway が clock を注入されていないためである。
// 鮮度が要る利用者は、自分の clock で計った実行区間と対応付ける。
type RateLimitSnapshot struct {
	Limit     int
	Remaining int
	// Reset は残量が回復する時刻である。header が無い response では zero value になる。
	Reset time.Time
}

// RateLimit は最後に観測した rate limit を返す。まだ観測していない場合は false を返す。
//
// 並行して request を実行してよい。返る値は defensive copy である。
func (g *Gateway) RateLimit() (RateLimitSnapshot, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.rateLimit, g.rateLimitObserved
}

// observeRateLimit は response header の残量を記録する。header を持たない response
// （GitHub 以外の proxy が返した error 等）では前回の観測を保持する。
func (g *Gateway) observeRateLimit(header http.Header) {
	remaining, err := strconv.Atoi(header.Get("X-RateLimit-Remaining"))
	if err != nil {
		return
	}
	snapshot := RateLimitSnapshot{Remaining: remaining}
	if limit, limitErr := strconv.Atoi(header.Get("X-RateLimit-Limit")); limitErr == nil {
		snapshot.Limit = limit
	}
	if reset, resetErr := strconv.ParseInt(header.Get("X-RateLimit-Reset"), 10, 64); resetErr == nil && reset > 0 {
		snapshot.Reset = time.Unix(reset, 0).UTC()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rateLimit = snapshot
	g.rateLimitObserved = true
}

// ListCandidateIssueRefs は candidate query の全 page を読み、identity だけを返す。
//
// body を返さないのは、polling の query result を実装入力にしないためである。
// Issue Contract は claim 直前の live read から compile する
// （docs/spec/05_design/04_github-routing.md の Polling fallback）。
//
// 引数が application 側の workflow.CandidateFilter なのは、この method が Controller の
// Discovery port を満たすためである。GitHub の query 語彙（label）と application の
// 語彙（ready label）の対応付けは、adapter であるここが行う。
func (g *Gateway) ListCandidateIssueRefs(ctx context.Context, filter workflow.CandidateFilter) ([]contract.IssueRef, error) {
	if err := filter.Validate(); err != nil {
		return nil, err
	}
	issues, err := g.ListCandidateIssues(ctx, CandidateFilter{Assignee: filter.Assignee, Label: filter.ReadyLabel})
	if err != nil {
		return nil, err
	}
	refs := make([]contract.IssueRef, 0, len(issues))
	for _, issue := range issues {
		ref, ok := g.issueRef(issue.Issue.Number)
		if !ok {
			return nil, invalidResponse("GET issues", "candidate Issue number が identity の範囲を超えた", nil)
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// ListOpenRunIssueRefs は open な kudo Pull Request の head branch から Run 中の Issue を
// 列挙する。
//
// 途中 phase の Run は candidate 条件（`ai-ready`）を満たさないため candidate query には
// 現れない。webhook を失った Run を polling が再開できるのは、この列挙が唯一の入口である。
// 判定は head branch 名だけで行い、PR body や label を読まない。何を次に行うかは
// ReconcileIssue が live state から導出する。
func (g *Gateway) ListOpenRunIssueRefs(ctx context.Context) ([]contract.IssueRef, error) {
	query := url.Values{
		"state":     {"open"},
		"sort":      {"created"},
		"direction": {"asc"},
	}
	seen := make(map[int64]struct{})
	var result []contract.IssueRef
	err := g.paginate(ctx, g.repositoryPath("pulls"), query, func(data []byte) error {
		var page []apiPullRequest
		if err := json.Unmarshal(data, &page); err != nil {
			return err
		}
		for _, value := range page {
			number, ok := workflow.IssueNumberFromBranch(value.Head.Ref)
			if !ok {
				continue
			}
			if _, exists := seen[number]; exists {
				continue
			}
			ref, valid := g.issueRef(number)
			if !valid {
				continue
			}
			seen[number] = struct{}{}
			result = append(result, ref)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// issueRef は gateway が束縛している repository の canonical identity で IssueRef を作る。
//
// GitHub の Issue number は int64、contract.IssueRef の Number は int である。
// 実在の Issue number がこの差を踏むことはないが、黙って truncate すると桁溢れした値が
// 別 Issue の identity になり、reconcile が取り違えた Issue を mutate し得る。
// 変換できない値は identity として使わない。
func (g *Gateway) issueRef(number int64) (contract.IssueRef, bool) {
	converted := int(number)
	if converted <= 0 || int64(converted) != number {
		return contract.IssueRef{}, false
	}
	return contract.IssueRef{
		Owner:      g.repository.Owner,
		Repository: g.repository.Name,
		Number:     converted,
	}, true
}

// EnsureIssueComment は Controller が名付けた record kind の comment を収束させる。
//
// GitHub 上の record を「同じものか」と判断する規則は adapter の所有であり、
// Controller は kind と本文だけを決める。
//
// この marker の Digest は本文の content digest ではなく、repository / Issue / kind から
// 決まる identity digest である。本文が収束対象の record（案内 comment）では、content
// digest を identity にすると文面を変えるたびに別 marker になり、同じ内容の comment が
// 増える。head や round に束縛されない Controller の記録だけがこの形を使い、evidence /
// verdict の check run は従来どおり content と head へ束縛した marker を使う。
func (g *Gateway) EnsureIssueComment(ctx context.Context, issueNumber int64, kind, body string) (bool, error) {
	if issueNumber <= 0 {
		return false, fmt.Errorf("Issue number は正数でなければならない")
	}
	marker := Marker{
		Repository: g.repository,
		Issue:      issueNumber,
		Kind:       kind,
		Digest:     recordIdentityDigest(g.repository, issueNumber, kind),
	}
	_, change, err := g.EnsureCommentContent(ctx, issueNumber, CommentRecord{Marker: marker, Body: body})
	if err != nil {
		return false, err
	}
	return change != CommentUnchanged, nil
}

// recordIdentityDigest は本文に依存しない record identity の digest を返す。
func recordIdentityDigest(repository Repository, issue int64, kind string) string {
	payload := recordIdentitySchema + "\n" +
		repository.String() + "\n" +
		strconv.FormatInt(issue, 10) + "\n" +
		kind + "\n"
	return string(contract.SHA256([]byte(payload)))
}

// recordIdentitySchema は identity digest の入力形式を version 付きで固定する。
// 形式を変えると過去の record が別 identity になるため、変更は marker の互換性判断を伴う。
const recordIdentitySchema = "kudo.record-identity/v1alpha1"
