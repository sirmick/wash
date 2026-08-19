# Agent Reconnect and History Plan

## Goal

Make Agent windows preserve their attached session across a browser reload,
allow detached live sessions to be reopened from the Agents rail with a
double-click, and make History available without starting a new session.

## Interaction Model

- Opening Agent from Apps shows the launcher. It does not start a session.
- History is available from the launcher.
- The Agents rail contains running sessions, including detached sessions.
- A single click on an attached row focuses its existing window.
- A double-click on a detached row opens one window on the existing session.
- Detach closes the window while leaving the session running.
- End session stops the adapter and leaves the conversation in History.
- Reload restores an attached Agent window and its transcript instead of
  showing the launcher.

## Implementation

1. Persist the authoritative attached session key from the `wash-ai` backend
   when `agentd` confirms a start or attach.
2. On frontend remount, restore that key and ask the backend to validate it.
3. Force an authoritative transcript replay for a remounted frontend, even
   when its app instance ID already has a live subscription in `agentd`.
4. Deliver frontend `started` state before requesting transcript replay so a
   fast snapshot cannot be cleared by initialization.
5. Clear stale persisted attachment state when validation fails.
6. Render the Agent menubar, including History, above the unstarted launcher.
7. Give detached rail rows explicit double-click reattach behavior and close
   attached Agent windows when Detach is selected from the desktop rail.
8. Make reattach idempotent in `agentd` so duplicate browser events cannot
   spawn duplicate Agent windows.

## Verification

- Component test attached-row focus and detached-row double-click behavior.
- Go test duplicate/stale reattach rejection where practical.
- E2E test browser reload restoring the active transcript and composer.
- E2E test detach, reload, double-click, and exactly one reopened window.
- E2E test opening History from an Agent launcher with no active session.

## Runtime Safety

Do not restart or terminate the developer's current wash/router session.
Builds and tests must run as isolated child processes using the test harness;
do not run installation, service-control, or live-session restart targets.
