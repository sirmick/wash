package workspacemcp

// leadOnly tools always fail for a launched member (the store refuses them), so
// a member's tool list leaves their schemas out of every turn's prompt.
var leadOnly = map[string]bool{"workspace_configure": true, "workspace_end": true, "message_retry": true}

// MemberTools is Tools without the orchestrator-only operations.
func MemberTools() []Tool {
	var out []Tool
	for _, t := range Tools() {
		if !leadOnly[t.Name] {
			out = append(out, t)
		}
	}
	return out
}

func Tools() []Tool {
	str := field("string")
	boolean := field("boolean")
	integer := field("integer")
	strings := map[string]any{"type": "array", "items": str, "maxItems": 100}
	array := func(items any) any { return map[string]any{"type": "array", "items": items, "maxItems": 100} }
	enum := func(values ...string) any { return map[string]any{"type": "string", "enum": values} }
	configs := map[string]any{"type": "object", "additionalProperties": str}
	profile := schema(map[string]any{"capability": enum("reviewer"), "approval": enum("ask", "auto"), "provider": str, "model": str, "thinking": str, "configs": configs, "subagents": enum("allow", "deny")}, "provider")
	nullable := func(s any) any { return map[string]any{"anyOf": []any{s, field("null")}} }
	qa := schema(map[string]any{"id": str, "action": enum("open", "reply", "assign", "block", "resolve", "reopen"), "package": str, "title": str, "assignee": str, "body": str, "blocking": boolean, "expected_revision": integer, "decision_refs": strings, "evidence": str}, "id", "action")
	// A message's qa only opens a thread, and the thread's body is the message's.
	open := schema(map[string]any{"id": str, "action": enum("open"), "package": str, "title": str, "assignee": str, "blocking": boolean, "decision_refs": strings}, "id", "action")
	msgProps := map[string]any{"recipient": str, "type": enum("instruction", "question", "answer", "progress"), "body": str, "reply_to": str, "assignment_id": str, "request_id": str, "thread_id": str, "qa": open}
	msg := schema(msgProps, "recipient", "type", "body")
	assignment := schema(map[string]any{"action": enum("create", "complete", "fail"), "id": str, "member_id": str, "text": str, "body": str, "request_id": str, "cc": strings}, "action")
	member := schema(map[string]any{"name": str, "profile": str, "capability": enum("reviewer"), "approval": enum("ask", "auto"), "provider": str, "model": str, "thinking": str, "configs": configs, "subagents": enum("allow", "deny"), "cwd": str, "instructions": str, "lifetime": enum("resident", "ephemeral"), "task": str, "can_spawn": boolean, "package": str, "role": enum("architect", "implementer", "reviewer")}, "name", "instructions", "lifetime")
	item := schema(map[string]any{"text": str, "emoji": str, "state": enum("pending", "active", "blocked", "done")})
	messageInput := map[string]any{"messages": array(msg), "request_id": str}
	// One message and a batch share the same tool.
	for k, v := range msgProps {
		messageInput[k] = v
	}
	return []Tool{
		{"workspace_get", "Read the workspace. Default view=team: per member state, activity, status, open assignments, undelivered mail and settings. view=state: full JSON with configuration, plan, assignments, pending decisions and each live session's config_options; include_messages with after/limit pages history. view=qa: QA Markdown and threads; package filters; thread_id pages events (after, limit 1–100). view=about: operating guide, usable before setup. Read-only.", schema(map[string]any{"view": enum("state", "about", "qa", "team"), "include_messages": boolean, "after": str, "limit": integer, "thread_id": str, "package": str})},
		{"workspace_configure", "Orchestrator: create (workspace.name; project_root defaults to cwd) or atomically patch the workspace. Omitted fields stay; null deletes. profiles replace by alias; plan.items patch by key; packages:{CODE:{title}} groups members under a title. members are keyed launch definitions: retries reuse a key, an existing one cannot change. qa_document.path creates or resumes a Markdown QA file, preserving ordinary Markdown; check qa_document_status for write errors. preview validates only; expected_revision guards. Configuration commits before launches: inspect each launch outcome and resume failures with member_control. capability:reviewer is provider-enforced read/search plus coordination; subagents:deny removes the provider's own subagent tool; unsupported adapters fail launch.", schema(map[string]any{"workspace": schema(map[string]any{"name": str, "project_root": str}, "name"), "name": str, "max_active": integer, "max_members": integer, "profiles": map[string]any{"type": "object", "additionalProperties": nullable(profile)}, "default_profile": str, "packages": map[string]any{"type": "object", "additionalProperties": nullable(schema(map[string]any{"title": str}, "title"))}, "members": map[string]any{"type": "object", "additionalProperties": member}, "plan": schema(map[string]any{"items": map[string]any{"type": "object", "additionalProperties": nullable(item)}, "order": map[string]any{"type": "array", "items": str, "maxItems": 500}}), "qa_document": nullable(schema(map[string]any{"path": str, "title": str}, "path")), "document": nullable(schema(map[string]any{"path": str, "title": str}, "path")), "expected_revision": integer, "request_id": str, "preview": boolean})},
		{"workspace_end", "Orchestrator: end the workspace's child sessions, keeping the owning conversation, files and history. Returns the final QA save status; failed saves keep retrying.", schema(nil)},
		{"member_control", "Pause, resume, interrupt or end members by ID/key, or a package (orchestrator). interrupt ends the current turn and leaves the member available (pause also stops dispatch until resumed). configure (orchestrator) changes a member's adapter settings, live and on every resume, e.g. configs:{mode:\"default\"} once you approve a plan-mode member's plan; members cannot leave plan mode themselves. Members may pause/resume/interrupt themselves; ending is orchestrator-only. Resume retries a failed launch. Returns per-member outcomes.", schema(map[string]any{"action": enum("pause", "resume", "end", "interrupt", "configure"), "member_ids": strings, "package": str, "configs": configs}, "action")},
		{"member_update", "Atomically set your status, complete/fail assignments and update QA. Results are summaries of at most 2000 bytes; put detail in QA or a file. waiting returns at once: then END YOUR TURN. waiting.until_assignments (ones you created) holds their results for one wake-up when the last resolves. A result may cc members, who get a non-waking copy. QA replies append; transitions need expected_revision; resolving needs evidence and orchestrator/package-reviewer authority.", schema(map[string]any{"status": str, "emoji": str, "waiting": schema(map[string]any{"reason": str, "reply_to": str, "until_assignments": strings}, "reason"), "assignment_results": array(assignment), "qa_updates": array(qa), "request_id": str})},
		{"message_send", "Send one message, or several atomically as messages. Recipients by member ID or key; a member's messages to the orchestrator are at most 2000 bytes. instruction/question/answer wake idle members; progress does not. qa:{action:open,id,package,title} opens a thread (assignee defaults to recipient); later replies carry thread_id.", schema(messageInput)},
		{"inbox_read", "Read your retained inbox history; new messages already arrive in your turn. after is a message ID; limit 1–100.", schema(map[string]any{"after": str, "limit": integer})},
		{"message_retry", "Orchestrator: retry an uncertain delivery after reconciling it; it may already have run.", schema(map[string]any{"id": str}, "id")},
		{"assignment_update", "Create assignments (spawning authority) or complete/fail your own. Residents stay for follow-up; ephemeral members retire after the turn.", schema(map[string]any{"updates": array(assignment), "request_id": str}, "updates")},
		{"decision_request", "Ask the human, with a recommendation and alternatives. thread_id links the question and answer into QA; the answer does not resolve it.", schema(map[string]any{"text": str, "thread_id": str, "request_id": str}, "text")},
		{"flash_message", "Display an attributed desktop-wide milestone/blocker notification without blocking work.", schema(map[string]any{"text": str, "emoji": str, "level": enum("info", "warning", "error")}, "text")},
	}
}
