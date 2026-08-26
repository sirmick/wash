// Which transcript frames this window is still willing to believe.
//
// Transcript snapshots and events ride the Bulk class (apps/ai/be/app.go),
// so that a streaming reply cannot sit in front of the window moves and
// keystrokes the human is making while the agent talks. The cost of a
// lower class is that a higher one can overtake it: `started` — what
// picking another row in the sessions pane sends — is Interactive, so it
// can land while frames for the session we just left are still queued.
//
// Without a guard those late frames are appended to the conversation the
// window has already switched to: the tail of one session pasted onto the
// top of another. The key is the frame's own word for what it belongs to.

/**
 * True when a transcript frame belongs to a session this window is no
 * longer showing, and should be dropped.
 *
 * A frame with no key is trusted (it predates the key being sent), and so
 * is any frame arriving before this window knows what it is showing: the
 * guard exists to reject the WRONG session, never the only one we have.
 */
export function isStaleTranscript(frameKey: unknown, sessionKey: string): boolean {
  const key = typeof frameKey === 'string' ? frameKey : '';
  return key !== '' && sessionKey !== '' && key !== sessionKey;
}
