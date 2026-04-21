package github

// PullRequest is the subset of the GitHub PR payload we care about. Field
// names match the GitHub REST response so shared/github can populate this
// from its httpGet directly.
//
// Lives in shared/github (not app/workflow) because shared/github's PR fetch
// method returns it, and the module boundary forbids shared from importing
// app. app/workflow imports this type via the `ghclient` alias.
type PullRequest struct {
	Number  int    `json:"number"`
	State   string `json:"state"` // "open" / "closed"
	Draft   bool   `json:"draft"`
	Merged  bool   `json:"merged"`
	Title   string `json:"title"`
	HTMLURL string `json:"html_url"`
	Head    struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
			CloneURL string `json:"clone_url"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}
