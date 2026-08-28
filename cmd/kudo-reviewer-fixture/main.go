// kudo-reviewer-fixture は Review Worker 開発用 repository に fixture PR を合成する。
// production image には含めず、明示した development credential でだけ起動する。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	githubadapter "github.com/mrbaron3/kudo/internal/adapter/github"
	"github.com/mrbaron3/kudo/internal/reviewerfixture"
)

const (
	fixtureTokenEnvironment = "KUDO_FIXTURE_GITHUB_TOKEN"
	usage                   = `Reviewer fixture PR seeder (development only)

Usage:
  kudo-reviewer-fixture --repository OWNER/REPOSITORY --issue NUMBER \
    --comment-author-id ID --check-run-app-id ID [--case CASE]

Cases:
  valid
  digest-mismatch
  missing-required-input
  missing-marker

Credential:
  KUDO_FIXTURE_GITHUB_TOKEN must contain the explicit development credential.
`
)

func main() {
	os.Exit(run(os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr))
}

func run(args []string, lookupEnv func(string) (string, bool), stdout, stderr io.Writer) int {
	if stdout == nil || stderr == nil {
		return 2
	}
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			fmt.Fprint(stdout, usage)
			return 0
		}
	}
	flags := flag.NewFlagSet("kudo-reviewer-fixture", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repositoryValue := flags.String("repository", "", "fixture repository (OWNER/REPOSITORY)")
	issueNumber := flags.Int64("issue", 0, "Task Issue number")
	caseName := flags.String("case", "valid", "fixture corpus case")
	commentAuthorID := flags.Int64("comment-author-id", 0, "Implementer bot user ID")
	checkRunAppID := flags.Int64("check-run-app-id", 0, "Implementer GitHub App ID")
	baseURL := flags.String("api-base-url", "", "GitHub API base URL")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "positional argument は指定できません")
		return 2
	}
	repository, err := parseRepository(*repositoryValue)
	if err != nil || *issueNumber <= 0 || *commentAuthorID <= 0 || *checkRunAppID <= 0 {
		fmt.Fprintln(stderr, "repository、正の issue、comment-author-id、check-run-app-id は必須です")
		return 2
	}
	fixture, err := reviewerfixture.LoadCase(*caseName)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if lookupEnv == nil {
		fmt.Fprintf(stderr, "%s を取得できません\n", fixtureTokenEnvironment)
		return 2
	}
	token, ok := lookupEnv(fixtureTokenEnvironment)
	if !ok || token == "" {
		fmt.Fprintf(stderr, "%s は必須です\n", fixtureTokenEnvironment)
		return 2
	}
	tokens, err := githubadapter.NewDevelopmentPATTokenSource(token)
	if err != nil {
		fmt.Fprintf(stderr, "%s が不正です\n", fixtureTokenEnvironment)
		return 2
	}
	gateway, err := githubadapter.NewGateway(http.DefaultClient, tokens, githubadapter.Config{
		BaseURL:    *baseURL,
		Repository: repository,
		RecorderIdentity: &githubadapter.RecorderIdentity{
			CommentAuthor: githubadapter.Actor{ID: *commentAuthorID, Login: "development-implementer"},
			CheckRunApp:   githubadapter.AppIdentity{ID: *checkRunAppID, Slug: "development-implementer", Name: "Development Implementer"},
		},
	})
	if err != nil {
		fmt.Fprintf(stderr, "GitHub gateway を構成できません: %v\n", err)
		return 2
	}
	target, err := reviewerfixture.NewLiveTarget(gateway)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	seeder, err := reviewerfixture.NewSeeder(target)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := seeder.Seed(ctx, reviewerfixture.SeedRequest{
		Repository: repository, Issue: *issueNumber, Fixture: fixture,
	})
	if err != nil {
		fmt.Fprintf(stderr, "fixture PR を合成できません: %v\n", err)
		return 1
	}
	output := struct {
		Fixture     string `json:"fixture"`
		PullRequest int    `json:"pullRequest"`
		Branch      string `json:"branch"`
		HeadSHA     string `json:"headSha"`
	}{
		Fixture: fixture.Name, PullRequest: result.PullRequest.Number,
		Branch: result.Head.BranchName, HeadSHA: result.Head.HeadSHA,
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(output); err != nil {
		fmt.Fprintf(stderr, "結果を出力できません: %v\n", err)
		return 1
	}
	return 0
}

func parseRepository(value string) (githubadapter.Repository, error) {
	if value == "" || value != strings.TrimSpace(value) || strings.Count(value, "/") != 1 {
		return githubadapter.Repository{}, errors.New("repository は OWNER/REPOSITORY 形式で指定する")
	}
	owner, name, _ := strings.Cut(value, "/")
	if owner == "" || name == "" || owner == "." || owner == ".." || name == "." || name == ".." ||
		strings.ContainsAny(value, "\\\n\r\t") {
		return githubadapter.Repository{}, errors.New("repository は OWNER/REPOSITORY 形式で指定する")
	}
	return githubadapter.Repository{Owner: strings.ToLower(owner), Name: strings.ToLower(name)}, nil
}
