package github

import (
	"context"
	"net/http"
)

type tokenSourceFunc func(context.Context) (string, error)

func (f tokenSourceFunc) Token(ctx context.Context) (string, error) { return f(ctx) }

func testGateway(client *http.Client, baseURL string) *Gateway {
	gateway, err := NewGateway(client, tokenSourceFunc(func(context.Context) (string, error) {
		return "actor-token", nil
	}), Config{
		BaseURL:    baseURL,
		APIVersion: "2026-03-10",
		Repository: Repository{Owner: "acme", Name: "widgets"},
	})
	if err != nil {
		panic(err)
	}
	return gateway
}
