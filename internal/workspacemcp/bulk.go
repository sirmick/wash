package workspacemcp

func allTools() []Tool { return append(Tools(), legacyTools()...) }
func Tools() []Tool {
	str := field("string")
	boolean := field("boolean")
	integer := field("integer")
	strings := map[string]any{"type": "array", "items": str, "maxItems": 100}
	array := func(items any) any { return map[string]any{"type": "array", "items": items, "maxItems": 100} }
	enum := func(values ...string) any { return map[string]any{"type": "string", "enum": values} }
	configs := map[string]any{"type": "object", "additionalProperties": str}
	profile := schema(map[string]any{"provider": str, "model": str, "thinking": str, "configs": configs}, "provider")
	nullable := func(s any) any { return map[string]any{"anyOf": []any{s, field("null")}} }
	qa := schema(map[string]any{"id": str, "action": enum("open", "reply", "assign", "block", "resolve", "reopen"), "package": str, "title": str, "assignee": str, "body": str, "blocking": boolean, "expected_revision": integer, "decision_refs": strings, "evidence": str}, "id", "action")
	msgProps := map[string]any{"recipient": str, "type": enum("instruction", "question", "answer", "progress"), "body": str, "reply_to": str, "assignment_id": str, "request_id": str, "thread_id": str, "qa": qa}
	msg := schema(msgProps, "recipient", "type", "body")
	assignment := schema(map[string]any{"action": enum("create", "complete", "fail"), "id": str, "member_id": str, "text": str, "body": str, "request_id": str}, "action")
	member := schema(map[string]any{"name": str, "profile": str, "provider": str, "model": str, "thinking": str, "configs": configs, "cwd": str, "instructions": str, "lifetime": enum("resident", "ephemeral"), "task": str, "can_spawn": boolean, "package": str, "role": enum("architect", "implementer", "reviewer")}, "name", "instructions", "lifetime")
	item := schema(map[string]any{"text": str, "emoji": str, "state": enum("pending", "active", "blocked", "done")})
	messageInput := map[string]any{"messages": array(msg), "request_id": str}
	// Accept the previous single-message shape, but advertise batching first.
	for k, v := range msgProps {
		messageInput[k] = v
	}
	return []Tool{
		{"workspace_get", "Read state, or view=about for operating instructions before setup. view=qa returns generated QA Markdown and threads; thread_id selects paginated events (after event ID, limit 1–100). State omits QA event bodies. package filters QA. No mutation or workspace creation.", schema(map[string]any{"view": enum("state", "about", "qa"), "include_messages": boolean, "after": str, "limit": integer, "thread_id": str, "package": str})},
		{"workspace_configure", "Set up or atomically patch a workspace. Initial setup requires workspace.name; project_root defaults to cwd. Omitted fields stay; profiles replace by alias (null deletes), plan.items patch by key (null removes), document:null clears. Members are keyed launch definitions: retries reuse; existing definitions cannot silently change. preview validates without mutation/launch. expected_revision guards; request_id deduplicates. Configuration commits before separate launch outcomes; resume failed launches explicitly. Package implementers/reviewers should be resident until accepted.", schema(map[string]any{"workspace": schema(map[string]any{"name": str, "project_root": str}, "name"), "name": str, "max_active": integer, "max_members": integer, "profiles": map[string]any{"type": "object", "additionalProperties": nullable(profile)}, "default_profile": str, "members": map[string]any{"type": "object", "additionalProperties": member}, "plan": schema(map[string]any{"items": map[string]any{"type": "object", "additionalProperties": nullable(item)}, "order": map[string]any{"type": "array", "items": str, "maxItems": 500}}), "document": nullable(schema(map[string]any{"path": str, "title": str}, "path")), "expected_revision": integer, "request_id": str, "preview": boolean})},
		{"workspace_end", "End the workspace's child sessions; preserve the owning conversation, files and history. Orchestrator only.", schema(nil)},
		{"member_control", "Pause, resume or end members by ID/key or package. Orchestrator manages packages; ending is orchestrator-only. Returns per-member outcomes. Resume can retry a failed reserved launch. Do not end package residents before acceptance.", schema(map[string]any{"action": enum("pause", "resume", "end"), "member_ids": strings, "package": str}, "action")},
		{"member_update", "Atomically report your status, acknowledge messages, complete/fail assignments and update QA. QA replies append; transitions require expected_revision. Resolution needs evidence and orchestrator/package-reviewer authority. waiting returns immediately: END YOUR TURN, do not poll. request_id makes retries safe.", schema(map[string]any{"status": str, "emoji": str, "waiting": schema(map[string]any{"reason": str, "reply_to": str}, "reason"), "acknowledge": strings, "assignment_results": array(assignment), "qa_updates": array(qa), "request_id": str})},
		{"message_send", "Send one or several messages atomically. Member IDs or keys are accepted. question/answer/instruction wake idle members; progress does not. Open a QA thread with qa:{action:open,id,package,title}; assignee defaults to recipient. Later replies carry thread_id and append attributed history. Use request_id to deduplicate retries.", schema(messageInput)},
		{"inbox_read", "Read your retained inbox; reading does not acknowledge it. after is a message ID; limit 1–100.", schema(map[string]any{"after": str, "limit": integer})},
		{"inbox_ack", "Atomically acknowledge received message IDs; does not complete assignments.", schema(map[string]any{"ids": strings, "request_id": str}, "ids")},
		{"message_retry", "Explicitly retry an uncertain delivery after reconciliation; execution may already have occurred. Orchestrator only.", schema(map[string]any{"id": str}, "id")},
		{"assignment_update", "Atomically create assignments (spawning authority) or complete/fail your own. Residents remain available for follow-up; ephemeral members retire after the turn. Use request_id to deduplicate retries.", schema(map[string]any{"updates": array(assignment), "request_id": str}, "updates")},
		{"decision_request", "Ask the human with recommendation and alternatives. Optional thread_id links the actual question and human answer into QA; an answer does not resolve QA. Use request_id for retries.", schema(map[string]any{"text": str, "thread_id": str, "request_id": str}, "text")},
		{"flash_message", "Display an attributed desktop-wide milestone/blocker notification without blocking work.", schema(map[string]any{"text": str, "emoji": str, "level": enum("info", "warning", "error")}, "text")},
	}
}
