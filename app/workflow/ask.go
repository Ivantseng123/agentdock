package workflow

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Ivantseng123/agentdock/app/config"
	ghclient "github.com/Ivantseng123/agentdock/shared/github"
	"github.com/Ivantseng123/agentdock/shared/logging"
	"github.com/Ivantseng123/agentdock/shared/metrics"
	"github.com/Ivantseng123/agentdock/shared/queue"
)

// AskPriorAnswerOptIn is the button value for the "include previous
// answer" opt-in on the description prompt. Exported so app/bot's
// HandleDescriptionAction can recognise it as a submit-path (no modal
// follows, so the selector message must be deleted on click).
const AskPriorAnswerOptIn = "帶上次回覆"

// AskWorkflow handles @bot ask queries. Optional attached repo with branch
// selection (mirrors Issue when channel has branch_select enabled), plus an
// optional description modal. Result is an agent-produced answer posted as a
// bot message in the thread.
type AskWorkflow struct {
	cfg       *config.Config
	slack     SlackPort
	repoCache *ghclient.RepoCache
	logger    *slog.Logger
}

type askState struct {
	Question       string // from args; empty = use thread only
	AttachRepo     bool
	SelectedRepo   string
	SelectedBranch string
	// PriorAnswer caches the bot's most recent substantive reply in this
	// thread (if any), fetched once at descriptionPromptStep time. nil means
	// either none exists, no fetch yet, or the fetch failed — which we
	// collapse into "no opt-in affordance".
	PriorAnswer *queue.ThreadMessage
	// IncludePriorAnswer is set by the user clicking the opt-in button on
	// the description prompt. BuildJob honours this to attach PriorAnswer
	// to the worker's PromptContext.
	IncludePriorAnswer bool
	// priorAnswerFetchAttempted guards against double-fetching if
	// descriptionPromptStep runs twice for the same ask (shouldn't happen
	// in current flow, but cheap defense against future back-nav changes).
	priorAnswerFetchAttempted bool

	// Multi-repo (ref) state. AddRefs is the user's yes/no on the decide
	// prompt; RefRepos accumulates as the user picks each ref (Branch is
	// filled later in the per-ref branch loop). RefBranchIdx steps the
	// per-ref branch picker forward. BranchTargetRepo is the transient
	// "which repo are we asking branches for right now" — set before each
	// branch select phase (primary OR ref) so BranchSelectedRepo can stay
	// a single-method interface (workflow.BranchStateReader) that doesn't
	// need to know about phases.
	AddRefs          bool
	RefRepos         []queue.RefRepo
	RefBranchIdx     int
	BranchTargetRepo string
}

// BranchSelectedRepo satisfies workflow.BranchStateReader so app/bot can
// read the repo off a Pending.State without depending on askState.
//
// Reads BranchTargetRepo (transient, set before each branch select phase)
// rather than SelectedRepo directly — for primary branch select we set
// BranchTargetRepo = SelectedRepo; for per-ref branch select we set it to
// the current ref's repo. This way BranchStateReader stays phase-agnostic.
func (s *askState) BranchSelectedRepo() string {
	if s == nil {
		return ""
	}
	return s.BranchTargetRepo
}

// RefExclusions returns the repos that should NOT appear as ref candidates:
// the primary plus any refs already picked. Used by HandleRefRepoSuggestion
// to filter type-ahead results when the channel has no static repo list.
// Body delegates to shared refExclusionsFor (also used by issueState).
func (s *askState) RefExclusions() []string {
	if s == nil {
		return nil
	}
	return refExclusionsFor(s.SelectedRepo, s.RefRepos)
}

// NewAskWorkflow constructs a workflow instance.
func NewAskWorkflow(cfg *config.Config, slack SlackPort, repoCache *ghclient.RepoCache, logger *slog.Logger) *AskWorkflow {
	if cfg == nil || slack == nil || logger == nil {
		panic("workflow: NewAskWorkflow missing required dep")
	}
	return &AskWorkflow{cfg: cfg, slack: slack, repoCache: repoCache, logger: logger}
}

// Type returns the TaskType discriminator.
func (w *AskWorkflow) Type() string { return "ask" }

// Trigger posts the attach-repo selector regardless of whether args has
// question text; if args is empty, the thread content is the question.
func (w *AskWorkflow) Trigger(ctx context.Context, ev TriggerEvent, args string) (NextStep, error) {
	// Populate common fields on the pending envelope — matches IssueWorkflow
	// so BuildJob can rely on p.RequestID / p.Reporter / p.ChannelName.
	reqID := logging.NewRequestID()
	reporter := w.slack.ResolveUser(ev.UserID)
	channelName := w.slack.GetChannelName(ev.ChannelID)

	pending := &Pending{
		ChannelID:   ev.ChannelID,
		ThreadTS:    ev.ThreadTS,
		TriggerTS:   ev.TriggerTS,
		UserID:      ev.UserID,
		Reporter:    reporter,
		ChannelName: channelName,
		RequestID:   reqID,
		Phase:       "ask_repo_prompt",
		TaskType:    "ask",
		State:       &askState{Question: args},
	}
	return w.attachPromptStep(pending), nil
}

// attachPromptStep builds the "附加 Repository?" selector used both on the
// initial Trigger and when the user backs out of the repo picker.
func (w *AskWorkflow) attachPromptStep(p *Pending) NextStep {
	return NextStep{
		Kind: NextStepSelector,
		Selector: &SelectorSpec{
			Prompt:   ":question: 要附加 Repository 嗎？",
			ActionID: "ask_attach_repo",
			Options: []SelectorOption{
				{Label: "附加", Value: "attach"},
				{Label: "不用", Value: "skip"},
			},
		},
		Pending: p,
	}
}

// Selection handles follow-up button clicks for the ask wizard. Six
// phases flow through here:
//   - ask_repo_prompt: attach/skip decision.
//   - ask_repo_select: user picked a specific repo, via button or external search.
//   - ask_branch_select: user picked a branch (only when branch_select enabled and repo has >1 branch).
//   - ask_prior_answer_prompt: yes/no on carrying the last bot answer (only when one exists).
//   - ask_description_prompt: optionally supplement the question via modal.
//   - ask_description_modal: modal submit; value is the text the user typed.
//
// Both skip-attach and repo-pick converge into priorAnswerOrDescriptionStep,
// which routes to either ask_prior_answer_prompt or straight to
// ask_description_prompt. Every ask therefore gets the chance to clarify
// what the agent should do — the D-selector path (empty args) especially
// needs this — otherwise the agent only sees the raw thread.
func (w *AskWorkflow) Selection(ctx context.Context, p *Pending, value string) (NextStep, error) {
	st, ok := p.State.(*askState)
	if !ok {
		return NextStep{Kind: NextStepError, ErrorText: "invalid pending state"}, nil
	}

	switch p.Phase {
	case "ask_repo_prompt":
		if value == "skip" {
			st.AttachRepo = false
			return w.priorAnswerOrDescriptionStep(p), nil
		}
		// "attach" → move to repo selection.
		st.AttachRepo = true
		channelCfg := w.cfg.ChannelDefaults
		if cc, ok := w.cfg.Channels[p.ChannelID]; ok {
			channelCfg = cc
		}
		repos := channelCfg.GetRepos()
		p.Phase = "ask_repo_select"
		if len(repos) == 0 {
			// No repos configured — fall back to external search. Back button
			// rides along so the user can bail to the attach prompt without
			// abandoning the whole flow.
			return NextStep{
				Kind: NextStepSelector,
				Selector: &SelectorSpec{
					Prompt:         ":point_right: Search and select a repo:",
					ActionID:       "ask_repo",
					Searchable:     true,
					Placeholder:    "Type to search repos...",
					CancelActionID: "ask_repo_back",
					CancelLabel:    "← 返回",
				},
				Pending: p,
			}, nil
		}
		options := make([]SelectorOption, 0, len(repos)+1)
		for _, r := range repos {
			options = append(options, SelectorOption{Label: r, Value: r})
		}
		options = append(options, SelectorOption{Label: "← 返回", Value: "back_to_attach"})
		return NextStep{
			Kind: NextStepSelector,
			Selector: &SelectorSpec{
				Prompt:   ":point_right: Which repo?",
				ActionID: "ask_repo",
				Options:  options,
			},
			Pending: p,
		}, nil

	case "ask_repo_select":
		if value == "back_to_attach" || value == "← 返回" {
			// User bailed out of the repo picker — reset the attach choice
			// and re-emit the attach/skip prompt so they can try again.
			st.AttachRepo = false
			st.SelectedRepo = ""
			p.Phase = "ask_repo_prompt"
			return w.attachPromptStep(p), nil
		}
		st.SelectedRepo = value
		return w.afterRepoSelectedStep(p), nil

	case "ask_branch_select":
		if value == "取消" {
			return NextStep{Kind: NextStepCancel}, nil
		}
		st.SelectedBranch = value
		return w.maybeAskRefStep(p), nil

	case "ask_ref_decide":
		if value == "skip" {
			st.AddRefs = false
			return w.priorAnswerOrDescriptionStep(p), nil
		}
		// "add" → enter the ref-pick loop.
		st.AddRefs = true
		return w.refPickStep(p), nil

	case "ask_ref_pick":
		// Cancellation (returning to attach prompt etc.) — bail out of refs
		// entirely and proceed to prior-answer/description. Match both the
		// static-button value ("back_to_decide") and the external-select
		// cancel button's label ("← 不加 ref"), since the slack adapter sends
		// the label as the action value for cancel buttons.
		if value == "back_to_decide" || value == "ask_ref_back" || value == "← 不加 ref" {
			st.AddRefs = false
			return w.priorAnswerOrDescriptionStep(p), nil
		}
		st.RefRepos = append(st.RefRepos, queue.RefRepo{
			Repo:     value,
			CloneURL: cleanCloneURL(value),
		})
		// Inline branch pick — ask THIS ref's branch immediately so the
		// repo+branch decision is one coherent step from the user's view.
		// Earlier design separated all picks from all branches, but that
		// asks the user to remember "did I want main for frontend, or for
		// backend?" 3 refs later. Inline matches primary's flow shape.
		st.RefBranchIdx = len(st.RefRepos) - 1
		return w.nextRefBranchStep(p), nil

	case "ask_ref_continue":
		switch value {
		case "more":
			return w.refPickStep(p), nil
		case "done":
			return w.priorAnswerOrDescriptionStep(p), nil
		default:
			return NextStep{Kind: NextStepError, ErrorText: fmt.Sprintf(":x: unexpected ref_continue value: %q", value)}, nil
		}

	case "ask_ref_branch":
		if value == "取消" {
			return NextStep{Kind: NextStepCancel}, nil
		}
		st.RefRepos[st.RefBranchIdx].Branch = value
		// Branch picked → loop pivot ("再加一個 / 完成").
		return w.refContinueStep(p), nil

	case "ask_prior_answer_prompt":
		// Standalone yes/no on whether to carry the bot's last substantive
		// reply into this turn. The cached PriorAnswer was populated in
		// priorAnswerOrDescriptionStep; we only need to flip the flag here.
		// Both answers continue to the description prompt — the two questions
		// are orthogonal, so neither subsumes the other.
		if value == AskPriorAnswerOptIn {
			st.IncludePriorAnswer = true
		}
		p.Phase = "ask_description_prompt"
		return w.descriptionPromptStep(p), nil

	case "ask_description_prompt":
		switch value {
		case "跳過":
			return NextStep{Kind: NextStepSubmit, Pending: p}, nil
		case "補充說明":
			// Phase must flip before OpenModal so the modal submit routes to
			// ask_description_modal (HandleDescriptionSubmit no longer rewrites
			// phase — it's workflow-owned now).
			p.Phase = "ask_description_modal"
			return NextStep{
				Kind:           NextStepOpenModal,
				ModalTitle:     "補充說明",
				ModalLabel:     "補充你想讓 agent 做什麼",
				ModalInputName: "description",
				ModalMetadata:  p.SelectorTS,
				Pending:        p,
			}, nil
		default:
			return NextStep{Kind: NextStepError, ErrorText: fmt.Sprintf(":x: unexpected description value: %q", value)}, nil
		}

	case "ask_description_modal":
		// value is the text the user submitted in the modal (empty on modal
		// close). Append to the original args-based Question so users who
		// typed `@bot ask <prefix>` can add more context without losing it.
		if value != "" {
			if st.Question != "" {
				st.Question = st.Question + "\n\n" + value
			} else {
				st.Question = value
			}
		}
		return NextStep{Kind: NextStepSubmit, Pending: p}, nil
	}

	return NextStep{Kind: NextStepError, ErrorText: fmt.Sprintf("unknown phase %q", p.Phase)}, nil
}

// afterRepoSelectedStep decides whether to show a branch selector or jump
// straight to the description prompt. Mirrors IssueWorkflow.afterRepoSelected:
// branch_select flag off, or ≤1 branch resolved, → skip branch step entirely.
// When repoCache is nil or branch listing fails, fall through to description
// prompt rather than erroring — branch is optional context for Ask.
func (w *AskWorkflow) afterRepoSelectedStep(p *Pending) NextStep {
	st := p.State.(*askState)

	channelCfg := w.cfg.ChannelDefaults
	if cc, ok := w.cfg.Channels[p.ChannelID]; ok {
		channelCfg = cc
	}

	if !channelCfg.IsBranchSelectEnabled() {
		return w.priorAnswerOrDescriptionStep(p)
	}

	var branches []string
	if len(channelCfg.Branches) > 0 {
		branches = channelCfg.Branches
	} else if w.repoCache != nil {
		ghToken := ""
		if w.cfg.Secrets != nil {
			ghToken = w.cfg.Secrets["GH_TOKEN"]
		}
		if repoPath, err := w.repoCache.EnsureRepo(st.SelectedRepo, ghToken); err == nil {
			if lb, listErr := w.repoCache.ListBranches(repoPath); listErr == nil {
				branches = lb
			}
		}
	}

	if len(branches) <= 1 {
		if len(branches) == 1 {
			st.SelectedBranch = branches[0]
		}
		return w.maybeAskRefStep(p)
	}

	st.BranchTargetRepo = st.SelectedRepo
	p.Phase = "ask_branch_select"
	options := make([]SelectorOption, 0, len(branches)+1)
	for _, b := range branches {
		options = append(options, SelectorOption{Label: b, Value: b})
	}
	options = append(options, SelectorOption{Label: "取消", Value: "取消"})
	return NextStep{
		Kind: NextStepSelector,
		Selector: &SelectorSpec{
			Prompt:           fmt.Sprintf(":point_right: Which branch of `%s`?", st.SelectedRepo),
			ActionID:         "ask_branch",
			Options:          options,
			MergeWithLastAck: true, // collapse "✅ repo" + "✅ branch" → "✅ repo, branch"
		},
		Pending: p,
	}
}

// priorAnswerOrDescriptionStep is the single entry into the post-repo/branch
// phases. It fetches the thread's most recent substantive bot reply once per
// ask; when one exists, it shows a dedicated 帶上次回覆 / 不用 selector so the
// opt-in is a standalone yes/no question. Otherwise it falls through to the
// 補充說明 / 跳過 prompt untouched. Splitting these used to live in one
// 3-button selector, but that forced users to pick opt-in XOR 補充說明 even
// though the choices are orthogonal.
//
// Fetch failures degrade silently to the no-prior-answer path — the feature
// is a convenience, not a core path (issue #151).
func (w *AskWorkflow) priorAnswerOrDescriptionStep(p *Pending) NextStep {
	st, _ := p.State.(*askState)
	if st != nil && !st.priorAnswerFetchAttempted {
		st.priorAnswerFetchAttempted = true
		if raw, err := w.slack.FetchPriorBotAnswer(p.ChannelID, p.ThreadTS, p.TriggerTS, w.cfg.MaxThreadMessages); err != nil {
			w.logger.Warn("prior bot answer fetch failed, continuing without opt-in",
				"phase", "處理中", "error", err)
		} else if raw != nil {
			st.PriorAnswer = &queue.ThreadMessage{
				User:      raw.User,
				Timestamp: raw.Timestamp,
				Text:      raw.Text,
			}
		}
	}

	if st != nil && st.PriorAnswer != nil {
		p.Phase = "ask_prior_answer_prompt"
		return NextStep{
			Kind: NextStepSelector,
			Selector: &SelectorSpec{
				Prompt:   ":arrows_counterclockwise: 要帶上次回覆一起問嗎？",
				ActionID: "ask_prior_answer",
				Options: []SelectorOption{
					{Label: "帶上次回覆", Value: AskPriorAnswerOptIn},
					{Label: "不用", Value: "不用"},
				},
			},
			Pending: p,
		}
	}

	p.Phase = "ask_description_prompt"
	return w.descriptionPromptStep(p)
}

// refCandidates returns the channel-allowed repo list minus primary and
// already-picked refs. Returns useExternalSearch=true when the channel uses
// type-ahead repo search instead of a static list — caller dispatches to
// the search-style selector with HandleRefRepoSuggestion as the suggestion
// handler.
func (w *AskWorkflow) refCandidates(p *Pending) (list []string, useExternalSearch bool) {
	st, _ := p.State.(*askState)
	cc := w.cfg.ChannelDefaults
	if c, ok := w.cfg.Channels[p.ChannelID]; ok {
		cc = c
	}
	repos := cc.GetRepos()
	if len(repos) == 0 {
		// External search: filtering happens in HandleRefRepoSuggestion.
		return nil, true
	}
	picked := make(map[string]bool, len(st.RefRepos))
	for _, r := range st.RefRepos {
		picked[r.Repo] = true
	}
	for _, r := range repos {
		if r == st.SelectedRepo || picked[r] {
			continue
		}
		list = append(list, r)
	}
	return list, false
}

// maybeAskRefStep is the single entry into the ref flow. Skips entirely
// when no candidate repos exist (channel has only primary) — keeps the
// thread free of the "加入參考 repo？" message in that case (spec AC-12).
func (w *AskWorkflow) maybeAskRefStep(p *Pending) NextStep {
	list, useExternalSearch := w.refCandidates(p)
	if !useExternalSearch && len(list) == 0 {
		return w.priorAnswerOrDescriptionStep(p)
	}
	p.Phase = "ask_ref_decide"
	return NextStep{
		Kind: NextStepSelector,
		Selector: &SelectorSpec{
			Prompt:   ":books: 加入參考 repo 嗎？(唯讀脈絡)",
			ActionID: "ask_ref_decide",
			Options: []SelectorOption{
				{Label: "加入", Value: "add"},
				{Label: "不用", Value: "skip"},
			},
		},
		Pending: p,
	}
}

// refPickStep posts the single-select picker for "next ref repo". Re-uses
// the existing selector infrastructure (button row / static_select /
// external_select auto-dispatch) — when the channel has no static list,
// falls back to type-ahead search via the ask_ref action_id which app/app.go
// routes to HandleRefRepoSuggestion (filters primary + already-picked).
func (w *AskWorkflow) refPickStep(p *Pending) NextStep {
	st := p.State.(*askState)
	list, useExternalSearch := w.refCandidates(p)
	p.Phase = "ask_ref_pick"
	prompt := ":point_right: 選參考 repo:"
	if len(st.RefRepos) > 0 {
		prompt = fmt.Sprintf(":point_right: 選下一個參考 repo（已加 %d 個）:", len(st.RefRepos))
	}
	if useExternalSearch {
		return NextStep{
			Kind: NextStepSelector,
			Selector: &SelectorSpec{
				Prompt:         prompt,
				ActionID:       "ask_ref",
				Searchable:     true,
				Placeholder:    "Type to search repos...",
				CancelActionID: "ask_ref_back",
				CancelLabel:    "← 不加 ref",
			},
			Pending: p,
		}
	}
	options := make([]SelectorOption, 0, len(list)+1)
	for _, r := range list {
		options = append(options, SelectorOption{Label: r, Value: r})
	}
	options = append(options, SelectorOption{Label: "← 不加 ref", Value: "back_to_decide"})
	return NextStep{
		Kind: NextStepSelector,
		Selector: &SelectorSpec{
			Prompt:   prompt,
			ActionID: "ask_ref",
			Options:  options,
		},
		Pending: p,
	}
}

// refContinueStep is the loop pivot after each ref pick: "再加一個 / 開始問問題".
// When the candidate pool is exhausted (static list and all picked), the
// "再加一個" option drops so the user can only proceed to the question.
func (w *AskWorkflow) refContinueStep(p *Pending) NextStep {
	st := p.State.(*askState)
	p.Phase = "ask_ref_continue"
	list, useExternalSearch := w.refCandidates(p)
	options := []SelectorOption{
		{Label: "開始問問題", Value: "done"},
	}
	// Allow another ref unless the static-list pool is fully consumed.
	// External search is unbounded so always offer "再加一個" there.
	if useExternalSearch || len(list) > 0 {
		options = append([]SelectorOption{{Label: "再加一個 ref", Value: "more"}}, options...)
	}
	return NextStep{
		Kind: NextStepSelector,
		Selector: &SelectorSpec{
			Prompt:   fmt.Sprintf(":heavy_plus_sign: 已加 %d 個 ref。", len(st.RefRepos)),
			ActionID: "ask_ref_continue",
			Options:  options,
		},
		Pending: p,
	}
}

// nextRefBranchStep handles the branch decision for the just-picked ref
// (st.RefRepos[RefBranchIdx]). Same skip rules as primary:
//   - branch_select disabled  → no picker, leave Branch empty
//   - ≤ 1 branch resolved     → auto-fill (or leave empty), no picker
//   - otherwise               → show picker
//
// In all "no picker" cases the next step is refContinueStep (loop pivot),
// not priorAnswerOrDescriptionStep — the user is always given a chance to
// add another ref or end the loop, even when individual branch choices
// are skipped.
func (w *AskWorkflow) nextRefBranchStep(p *Pending) NextStep {
	st := p.State.(*askState)
	target := st.RefRepos[st.RefBranchIdx].Repo

	cc := w.cfg.ChannelDefaults
	if c, ok := w.cfg.Channels[p.ChannelID]; ok {
		cc = c
	}
	if !cc.IsBranchSelectEnabled() {
		return w.refContinueStep(p)
	}

	var branches []string
	if len(cc.Branches) > 0 {
		branches = cc.Branches
	} else if w.repoCache != nil {
		ghToken := ""
		if w.cfg.Secrets != nil {
			ghToken = w.cfg.Secrets["GH_TOKEN"]
		}
		if rp, err := w.repoCache.EnsureRepo(target, ghToken); err == nil {
			if lb, lerr := w.repoCache.ListBranches(rp); lerr == nil {
				branches = lb
			}
		}
	}
	if len(branches) <= 1 {
		if len(branches) == 1 {
			st.RefRepos[st.RefBranchIdx].Branch = branches[0]
		}
		return w.refContinueStep(p)
	}

	st.BranchTargetRepo = target
	p.Phase = "ask_ref_branch"
	options := make([]SelectorOption, 0, len(branches)+1)
	for _, b := range branches {
		options = append(options, SelectorOption{Label: b, Value: b})
	}
	options = append(options, SelectorOption{Label: "取消", Value: "取消"})
	return NextStep{
		Kind: NextStepSelector,
		Selector: &SelectorSpec{
			Prompt:           fmt.Sprintf(":point_right: Which branch of ref `%s`?", target),
			ActionID:         "ask_ref_branch",
			Options:          options,
			MergeWithLastAck: true, // collapse ref's "✅ repo" + "✅ branch" → "✅ repo, branch"
		},
		Pending: p,
	}
}

// descriptionPromptStep returns the 補充說明 / 跳過 selector. The opt-in
// 帶上次回覆 button used to live here but was promoted to its own phase —
// see priorAnswerOrDescriptionStep for the routing wrapper. ActionID mirrors
// IssueWorkflow's so app.go's description_action route (which owns the
// modal-trigger-id forwarding) handles the click without Ask needing its
// own special case.
func (w *AskWorkflow) descriptionPromptStep(p *Pending) NextStep {
	return NextStep{
		Kind: NextStepSelector,
		Selector: &SelectorSpec{
			Prompt:   ":pencil2: 要補充說明想讓 agent 做什麼嗎？",
			ActionID: "description_action",
			Options: []SelectorOption{
				{Label: "補充說明", Value: "補充說明"},
				{Label: "跳過", Value: "跳過"},
			},
		},
		Pending: p,
	}
}

// BuildJob assembles the queue.Job from the completed pending state.
// Status text is the message posted while the worker runs.
func (w *AskWorkflow) BuildJob(ctx context.Context, p *Pending) (*queue.Job, string, error) {
	st, ok := p.State.(*askState)
	if !ok {
		return nil, "", fmt.Errorf("AskWorkflow.BuildJob: unexpected state type")
	}

	reqID := p.RequestID
	if reqID == "" {
		reqID = logging.NewRequestID()
	}

	cloneURL := ""
	if st.AttachRepo && st.SelectedRepo != "" {
		cloneURL = cleanCloneURL(st.SelectedRepo)
	}

	var priorAnswer []queue.ThreadMessage
	if st.IncludePriorAnswer && st.PriorAnswer != nil {
		priorAnswer = []queue.ThreadMessage{*st.PriorAnswer}
	}

	// Output rules injection: when refs are attached, append two hard rules
	// (spec §4.6 Layer 2). Read-only enforcement + critical-fail-fast match
	// SKILL.md §5's Reference repos section. Inject regardless of whether
	// the refs successfully cloned — user intent was ref-aware ask, so the
	// rules are relevant even if all refs end up unavailable.
	outputRules := w.cfg.Workflows.Ask.Prompt.OutputRules
	if len(st.RefRepos) > 0 {
		outputRules = append(outputRules,
			"不可寫入、修改、刪除 <ref_repos> 列出之任何 path 之下的檔案；refs 為唯讀脈絡。",
			"若 <unavailable_refs> 含關鍵 ref，必須在答案開頭聲明「無法取得 X repo 脈絡，無法回答」並停手；不要 best-effort 拼湊。",
		)
	}

	job := &queue.Job{
		ID:          reqID,
		RequestID:   reqID,
		TaskType:    "ask",
		ChannelID:   p.ChannelID,
		ThreadTS:    p.ThreadTS,
		UserID:      p.UserID,
		Repo:        st.SelectedRepo,
		Branch:      st.SelectedBranch,
		CloneURL:    cloneURL,
		RefRepos:    st.RefRepos,
		SubmittedAt: time.Now(),
		PromptContext: &queue.PromptContext{
			Goal:             w.cfg.Workflows.Ask.Prompt.Goal,
			ResponseSchema:   w.cfg.Workflows.Ask.Prompt.ResponseSchema,
			OutputRules:      outputRules,
			Language:         w.cfg.PromptDefaults.Language,
			ExtraDescription: st.Question,
			Branch:           st.SelectedBranch,
			Channel:          p.ChannelName,
			Reporter:         p.Reporter,
			AllowWorkerRules: w.cfg.PromptDefaults.IsWorkerRulesAllowed(),
			PriorAnswer:      priorAnswer,
			// ThreadMessages, Attachments, RefRepos, UnavailableRefs filled
			// by downstream — ThreadMessages/Attachments by submit-helper,
			// RefRepos/UnavailableRefs by worker after PrepareAt resolves
			// per-ref absolute paths.
		},
		// Skills is populated by submitJob via loadSkills(); leaving it unset
		// here (instead of Skills: nil) avoids the misleading impression that
		// Ask opts out — the override in app.go:286 applies to every workflow.
	}
	return job, ":thinking_face: 思考中...", nil
}

const askMaxChars = 38000

// askFallbackBanner is the transparency banner prepended to answers
// produced by the missing-marker fallback path. Spec §Slack Rendering.
const askFallbackBanner = ":warning: 請驗證輸出答案,AGENT 並未遵守輸出格式\n\n"

// askInlineThreshold is the char length above which the answer goes into
// an uploaded .md file instead of the Slack message body. 2000 keeps most
// replies inline while packaging long-form answers so the channel isn't
// swamped. Matches where Slack's own text readability starts to degrade.
const askInlineThreshold = 2000

// HandleResult renders the agent output into the Slack thread. Failure paths
// are posted without a retry button (Ask is best-effort). Parse failures and
// answers are both final — no retry lane.
func (w *AskWorkflow) HandleResult(ctx context.Context, state *queue.JobState, r *queue.JobResult) error {
	if state == nil || state.Job == nil {
		return fmt.Errorf("HandleResult: state or state.Job is nil")
	}
	job := state.Job

	if r.Status == "failed" {
		text := fmt.Sprintf(":x: 思考失敗：%s", r.Error)
		return w.post(job, text)
	}

	parsed, err := ParseAskOutput(r.RawOutput)
	if err != nil {
		truncated := logging.Redact(r.RawOutput, w.cfg.Secrets)
		if len(truncated) > 2000 {
			truncated = truncated[:2000] + "…(truncated)"
		}
		w.logger.Warn("ask parse failed", "phase", "失敗", "output", truncated, "err", err)
		metrics.WorkflowCompletionsTotal.WithLabelValues("ask", "parse_failed").Inc()
		// err != nil only when stdout fails the syntactic gate (truly empty
		// or below askFallbackMinLength). Every other parse-failure shape now
		// routes through fallback with a fallback_* ResultSource. Ask remains
		// best-effort with no retry lane; r.Status stays "completed" so the
		// listener clears dedup. Spec 2026-04-26-ask-fallback-extension §HandleResult.
		return w.post(job, ":x: Agent 沒有產生任何答案")
	}

	// Redact configured secrets before posting to Slack (#180).
	answer := logging.Redact(parsed.Answer, w.cfg.Secrets)
	status := "success"
	if parsed.ResultSource != ResultSourceSchema {
		answer = askFallbackBanner + answer
		status = parsed.ResultSource
	}

	if len(answer) > askMaxChars {
		answer = answer[:askMaxChars] + "\n…(已截斷)"
	}

	metrics.WorkflowCompletionsTotal.WithLabelValues("ask", status).Inc()
	return w.post(job, answer)
}

// post writes the answer into the thread. Short answers replace the
// status message inline; answers over askInlineThreshold are uploaded as
// an answer.md file with a short preview comment, because long bodies
// render awkwardly in Slack's message column and agent output is
// mrkdwn-styled (which collapses its own structure at length).
//
// When file upload fails (e.g. workspace missing the files:write scope
// for files.upload v2), the answer is posted inline as a fallback so the
// user still sees the content — truncated messages are preferable to a
// dangling "已附為檔案：" preview with no file.
func (w *AskWorkflow) post(job *queue.Job, text string) error {
	if len(text) <= askInlineThreshold {
		if job.StatusMsgTS != "" {
			return w.slack.UpdateMessage(job.ChannelID, job.StatusMsgTS, text)
		}
		return w.slack.PostMessage(job.ChannelID, text, job.ThreadTS)
	}

	preview := fmt.Sprintf(":memo: 答案較長（約 %d 字），已附為檔案：", len(text))
	var uploadErr error
	if job.StatusMsgTS != "" {
		// Flip the earlier "思考中..." status into the preview so the
		// thread has one lifecycle marker + the file, not two notices.
		_ = w.slack.UpdateMessage(job.ChannelID, job.StatusMsgTS, preview)
		uploadErr = w.slack.UploadFile(job.ChannelID, job.ThreadTS, "answer.md", "Answer", text, "")
	} else {
		uploadErr = w.slack.UploadFile(job.ChannelID, job.ThreadTS, "answer.md", "Answer", text, preview)
	}
	if uploadErr == nil {
		return nil
	}

	w.logger.Warn("Slack 檔案上傳失敗，改用內嵌訊息",
		"phase", "失敗", "error", uploadErr, "length", len(text))
	if job.StatusMsgTS != "" {
		_ = w.slack.UpdateMessage(job.ChannelID, job.StatusMsgTS, ":memo: 答案如下：")
	}
	return w.slack.PostMessage(job.ChannelID, text, job.ThreadTS)
}
