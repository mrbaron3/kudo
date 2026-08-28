//go:build githublive

package reviewerfixture

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	githubadapter "github.com/mrbaron3/kudo/internal/adapter/github"
)

// TestLiveReviewerFixtureSeed は明示した fixture repository に実際の draft PR を作る。
// opt-in tag と専用 credential の両方を要求し、通常の unit/check 経路では実行しない。
func TestLiveReviewerFixtureSeed(t *testing.T) {
	repository := liveRepository(t, requiredLiveEnvironment(t, "KUDO_FIXTURE_REPOSITORY"))
	issueNumber := requiredPositiveInt64(t, "KUDO_FIXTURE_ISSUE_NUMBER")
	commentAuthorID := requiredPositiveInt64(t, "KUDO_FIXTURE_IMPLEMENTER_COMMENT_AUTHOR_ID")
	checkRunAppID := requiredPositiveInt64(t, "KUDO_FIXTURE_IMPLEMENTER_CHECK_RUN_APP_ID")
	token, err := githubadapter.NewDevelopmentPATTokenSource(requiredLiveEnvironment(t, "KUDO_FIXTURE_GITHUB_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := githubadapter.NewGateway(http.DefaultClient, token, githubadapter.Config{
		Repository: repository,
		RecorderIdentity: &githubadapter.RecorderIdentity{
			CommentAuthor: githubadapter.Actor{ID: commentAuthorID, Login: "development-implementer"},
			CheckRunApp:   githubadapter.AppIdentity{ID: checkRunAppID, Slug: "development-implementer", Name: "Development Implementer"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := NewLiveTarget(gateway)
	if err != nil {
		t.Fatal(err)
	}
	seeder, err := NewSeeder(target)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := LoadCase("valid")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	request := SeedRequest{Repository: repository, Issue: issueNumber, Fixture: fixture}

	first, err := seeder.Seed(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := seeder.Seed(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if !first.PullRequest.Draft || first.PullRequest.Number != second.PullRequest.Number ||
		first.Head.HeadSHA != second.Head.HeadSHA || first.TestPlan.ID != second.TestPlan.ID ||
		first.RedEvidence.ID != second.RedEvidence.ID {
		t.Fatalf("live fixture が再実行で収束しない: first=%#v second=%#v", first, second)
	}
	if first.RedEvidence.App.ID != checkRunAppID || first.TestPlan.Author.ID != commentAuthorID {
		t.Fatalf("live fixture identity が Implementer 設定と一致しない: comment=%#v check=%#v",
			first.TestPlan.Author, first.RedEvidence.App)
	}
}

func requiredLiveEnvironment(t *testing.T, key string) string {
	t.Helper()
	value, ok := os.LookupEnv(key)
	if !ok || value == "" || value != strings.TrimSpace(value) {
		t.Fatalf("%s が欠落または不正", key)
	}
	return value
}

func requiredPositiveInt64(t *testing.T, key string) int64 {
	t.Helper()
	value, err := strconv.ParseInt(requiredLiveEnvironment(t, key), 10, 64)
	if err != nil || value <= 0 {
		t.Fatalf("%s は正の整数でなければならない", key)
	}
	return value
}

func liveRepository(t *testing.T, value string) githubadapter.Repository {
	t.Helper()
	if strings.Count(value, "/") != 1 {
		t.Fatalf("KUDO_FIXTURE_REPOSITORY は OWNER/REPOSITORY 形式でなければならない")
	}
	owner, name, _ := strings.Cut(value, "/")
	if owner == "" || name == "" {
		t.Fatalf("KUDO_FIXTURE_REPOSITORY は OWNER/REPOSITORY 形式でなければならない")
	}
	return githubadapter.Repository{Owner: owner, Name: name}
}
