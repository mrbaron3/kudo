//go:build githublive

package github

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

type liveClock struct{}

func (liveClock) Now() time.Time { return time.Now() }

// TestLiveGitHubAppInstallationTokens は actor ごとに実在する App installation から
// permission subset 付き token を発行し、その token が対象 repository を読めることを確認する。
// build tag で opt-in 済みのため、credential 設定の欠落は skip せず failure にする。
func TestLiveGitHubAppInstallationTokens(t *testing.T) {
	sources := make(map[ActorRole]*AppTokenSource, 2)
	for _, actor := range []ActorRole{ActorImplementer, ActorReviewer} {
		config, err := AppTokenSourceConfigFromEnvironment(actor, os.LookupEnv)
		if err != nil {
			t.Fatalf("%s GitHub App の live test 設定が不正: %v", actor, err)
		}
		source, err := NewAppTokenSource(http.DefaultClient, liveClock{}, config)
		if err != nil {
			t.Fatalf("%s GitHub App token source を構成できない: %v", actor, err)
		}
		sources[actor] = source
	}
	implementer := sources[ActorImplementer]
	reviewer := sources[ActorReviewer]
	if implementer.appID == reviewer.appID ||
		implementer.installationID == reviewer.installationID ||
		implementer.privateKey.N.Cmp(reviewer.privateKey.N) == 0 {
		t.Fatal("Implementer と Reviewer の live credential が分離されていない")
	}

	for _, actor := range []ActorRole{ActorImplementer, ActorReviewer} {
		t.Run(string(actor), func(t *testing.T) {
			source := sources[actor]

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := source.Token(ctx); err != nil {
				t.Fatalf("%s installation token を発行できない: %v", actor, err)
			}

			gateway, err := NewGateway(http.DefaultClient, source, Config{
				Repository: Repository{Owner: "mrbaron3", Name: "kudo"},
			})
			if err != nil {
				t.Fatalf("%s GitHub gateway を構成できない: %v", actor, err)
			}
			content, err := gateway.ReadContent(ctx, "README.md", "main")
			if err != nil {
				t.Fatalf("%s token で kudo/README.md を読めない: %v", actor, err)
			}
			if content.Path != "README.md" || content.SHA == "" || len(content.Data) == 0 {
				t.Fatalf("%s token の repository content response が不正", actor)
			}
		})
	}
}
