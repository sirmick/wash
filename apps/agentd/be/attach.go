// Attachments on a prompt (docs/Review-findings.md P2 → agent: "attach
// files/images or paste an image").
//
// A composer that only sends text makes the commonest debugging move —
// "here is the screenshot, what is wrong?" — impossible, even though both
// adapters advertise promptCapabilities.image and have since wash first
// spoke ACP.
//
// Two shapes, and the difference matters:
//
//	image → an ACP image block, base64 bytes inline. The bytes came from
//	        the user's clipboard and exist nowhere on disk, so by-value is
//	        the only option; hence the size cap.
//	file  → an ACP resource_link, a file:// reference. NOT the bytes: the
//	        agent reads it with its own tools, through the same fs
//	        confinement and the same permission ask as any other read, so
//	        attaching a file does not smuggle its contents past the
//	        approval the user would otherwise have been asked for.
//
// Everything a window sends is validated here rather than trusted. A
// window names a mime type and a path; neither is authority.
package agentd

import (
	"encoding/base64"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirmick/wash/internal/acp"
)

// maxAttachImageBytes bounds ONE pasted image, measured after decoding.
// 2 MB is a generous full-screen PNG and well under what an adapter will
// accept; past it the paste is refused with a note rather than sent, on
// the principle that a prompt silently truncated is worse than one that
// says why it was not sent.
const maxAttachImageBytes = 2 << 20

// maxAttachments bounds how many blocks one prompt may carry.
const maxAttachments = 16

// attachmentBlocks turns what a window sent into ACP content blocks,
// dropping anything that does not validate. It also puts what was
// attached into the transcript — a pasted image is shown, a linked file
// is named — so the conversation records what the agent was actually
// given rather than only what was typed.
func (h *hosted) attachmentBlocks(list []promptAttachment) []acp.ContentBlock {
	if len(list) == 0 {
		return nil
	}
	if len(list) > maxAttachments {
		log.Printf("agentd: attach key=%s dropping %d over the %d limit", h.key, len(list)-maxAttachments, maxAttachments)
		list = list[:maxAttachments]
	}
	fsys := h.fsFor()
	now := time.Now()
	var out []acp.ContentBlock
	for _, a := range list {
		switch a.Type {
		case "image":
			if !strings.HasPrefix(a.Mime, "image/") || a.Data == "" {
				log.Printf("agentd: attach key=%s refused image mime=%q", h.key, a.Mime)
				continue
			}
			// Decoded length, not base64 length: the cap is about the
			// bytes the adapter receives.
			n := base64.StdEncoding.DecodedLen(len(a.Data))
			if n > maxAttachImageBytes {
				log.Printf("agentd: attach key=%s refused image bytes=%d over=%d", h.key, n, maxAttachImageBytes)
				h.note("That image is about " + itoa(uint64(n>>10)) + " KB, over the " + itoa(uint64(maxAttachImageBytes>>10)) + " KB paste limit. It was not sent.")
				continue
			}
			out = append(out, acp.Image(a.Mime, a.Data))
			h.pushAttachEvent(Event{Kind: EventImage, Mime: a.Mime, Text: a.Data}, now)

		case "file":
			abs, err := fsys.Confine(a.Path)
			if err != nil {
				log.Printf("agentd: attach key=%s refused path=%q root=%q: %v", h.key, a.Path, h.cwd, err)
				h.note("Cannot attach " + a.Path + ": it is outside this session's folder.")
				continue
			}
			if st, err := os.Stat(abs); err != nil || st.IsDir() {
				log.Printf("agentd: attach key=%s unreadable path=%s", h.key, abs)
				continue
			}
			name := a.Name
			if name == "" {
				name = filepath.Base(abs)
			}
			out = append(out, acp.ResourceLink(fileURI(abs), name))
			h.pushAttachEvent(Event{
				Kind: EventTool, ToolKind: "attach", Title: name,
				Path: abs, Status: "completed",
			}, now)

		default:
			log.Printf("agentd: attach key=%s unknown type=%q", h.key, a.Type)
		}
	}
	if len(out) > 0 {
		log.Printf("agentd: attach key=%s blocks=%d", h.key, len(out))
	}
	return out
}

// pushAttachEvent records an attachment in the transcript and shows it to
// every watcher. Recorded, not merely shown: a reloaded window must see
// the screenshot it sent, and the stored transcript is what a resumed
// session reads back.
func (h *hosted) pushAttachEvent(e Event, now time.Time) {
	stored := appendEvent(h.key, e, now)
	if h.conn != nil {
		pushEvent(h.conn, h.key, stored)
	}
}

// fileURI renders an absolute path as the file:// URL a resource_link
// carries. Built with url.URL rather than by concatenation so a path with
// a space or a # in it survives.
func fileURI(abs string) string {
	u := url.URL{Scheme: "file", Path: abs}
	return u.String()
}
