package workflow

import (
	"fmt"

	"github.com/Ivantseng123/agentdock/app/dispatch"
	"github.com/Ivantseng123/agentdock/app/githubapp"
)

type repoAuthChoice struct {
	source        githubapp.TokenSource
	authSource    string
	perCallToken  string
	patFallback   bool
}

func chooseRepoAuth(repo, patToken string, source githubapp.TokenSource) (repoAuthChoice, error) {
	if source == nil {
		return repoAuthChoice{}, fmt.Errorf("github auth source not configured")
	}
	chosen, fallback, err := dispatch.ChooseJobSource(patToken, source, repo)
	if err != nil {
		return repoAuthChoice{}, err
	}
	choice := repoAuthChoice{
		source:      chosen,
		authSource:  "app",
		patFallback: fallback,
	}
	if githubapp.IsPATSource(chosen) {
		choice.authSource = "pat"
	}
	if fallback {
		choice.authSource = "pat_fallback"
		choice.perCallToken = patToken
	}
	return choice, nil
}

func repoAccessError(repo string) string {
	return fmt.Sprintf("無法存取 repo %s：GitHub App 未涵蓋此 repo，且未設定 PAT fallback", repo)
}

func headRepoAccessError(repo string) error {
	return fmt.Errorf("PR 可讀，但無法存取 head repo %s；請安裝 GitHub App 到該 repo 擁有者或設定 PAT fallback", repo)
}
