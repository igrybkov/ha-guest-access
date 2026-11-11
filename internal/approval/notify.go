package approval

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// NotificationID is the HA persistent notification id of request
// requestID. It is stable, so the dismiss finds the notification the
// create made.
func NotificationID(requestID string) string { return "guest_access_" + requestID }

// NotifierOptions configure a Notifier. CallService is required.
type NotifierOptions struct {
	// CallService calls an HA service with no target, such as
	// persistent_notification.create.
	CallService func(ctx context.Context, domain, service string, data map[string]any) error
	// PanelLink returns the path of the admin page's pending panel in HA's
	// frontend. nil means there is none (outside Supervisor), and the
	// notification has no link. An error also means no link this time;
	// the next notification asks again.
	PanelLink func(ctx context.Context) (string, error)
	// Timeout caps one HA call, and one PanelLink call. Pass the HA event
	// timeout.
	Timeout time.Duration
	Logger  *slog.Logger
}

// Notifier shows each waiting device request as an HA persistent
// notification and dismisses it when the request ends: approved, denied,
// expired or reset.
//
// The Manager queues work under its lock, so the queue has the order of
// the state changes. Run sends the queue to HA from one goroutine, so a
// dismiss never overtakes its create. HA calls never block a guest or an
// admin request. A failed call is logged and has no effect on the
// request. Methods on a nil *Notifier do nothing.
type Notifier struct {
	opts NotifierOptions
	log  *slog.Logger

	mu    sync.Mutex
	queue []notifyOp
	wake  chan struct{}

	link string // the panel link, once PanelLink gave one. Only Run uses it.
}

type notifyOp struct {
	create   bool
	req      Request
	linkName string
}

// NewNotifier returns a Notifier. It panics without CallService.
func NewNotifier(o NotifierOptions) *Notifier {
	if o.CallService == nil {
		panic("approval: NotifierOptions.CallService is required")
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultEventTimeout
	}
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Notifier{opts: o, log: log.With("component", "notify"), wake: make(chan struct{}, 1)}
}

// created queues the notification of a new request. It never blocks.
func (n *Notifier) created(req Request, linkName string) {
	n.push(notifyOp{create: true, req: req, linkName: linkName})
}

// ended queues the dismiss of a request's notification. It never blocks.
func (n *Notifier) ended(req Request) {
	n.push(notifyOp{req: req})
}

func (n *Notifier) push(op notifyOp) {
	if n == nil {
		return
	}
	n.mu.Lock()
	n.queue = append(n.queue, op)
	n.mu.Unlock()
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

// Run sends queued notifications to HA, in order, until ctx is done.
// Work still queued then is dropped: HA keeps any notification it has,
// and the user can dismiss it there.
func (n *Notifier) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-n.wake:
		}
		for {
			n.mu.Lock()
			ops := n.queue
			n.queue = nil
			n.mu.Unlock()
			if len(ops) == 0 {
				break
			}
			for _, op := range ops {
				if ctx.Err() != nil {
					return
				}
				n.send(ctx, op)
			}
		}
	}
}

func (n *Notifier) send(ctx context.Context, op notifyOp) {
	id := NotificationID(op.req.ID)
	service, data := "dismiss", map[string]any{"notification_id": id}
	if op.create {
		service = "create"
		data["title"] = notificationTitle(op.linkName)
		data["message"] = notificationMessage(op.req, n.panelLink(ctx))
	}
	cctx, cancel := context.WithTimeout(ctx, n.opts.Timeout)
	defer cancel()
	if err := n.opts.CallService(cctx, "persistent_notification", service, data); err != nil {
		n.log.Warn("HA notification", "service", "persistent_notification."+service, "request", op.req.ID, "err", err)
	}
}

// panelLink returns the pending panel's path, or "" when there is none.
func (n *Notifier) panelLink(ctx context.Context) string {
	if n.link != "" || n.opts.PanelLink == nil {
		return n.link
	}
	cctx, cancel := context.WithTimeout(ctx, n.opts.Timeout)
	defer cancel()
	link, err := n.opts.PanelLink(cctx)
	if err != nil {
		n.log.Warn("no app link for the HA notification", "err", err)
		return ""
	}
	n.link = link
	return link
}

// notificationTitle is the notification's title. HA's frontend shows the
// title as plain text, so it is not escaped. It holds only the link name,
// which the admin typed; guest text stays in the message.
func notificationTitle(linkName string) string {
	return "Someone wants in: " + Clean(linkName)
}

// notificationMessage is the notification's markdown body. The label and
// device come from the guest, so every markdown control character in them
// is escaped (see escapeMarkdown). link is the pending panel's path, or
// "" for no link.
func notificationMessage(req Request, link string) string {
	var b strings.Builder
	if req.Label != "" {
		b.WriteString("Guest: " + escapeMarkdown(req.Label) + "\n")
	}
	b.WriteString("Device: " + escapeMarkdown(req.Device) + "\n")
	b.WriteString("Code: **" + escapeMarkdown(req.Code) + "**\n")
	b.WriteString("\nCheck that the guest sees the same code.")
	if link != "" {
		b.WriteString("\n\n[Approve or deny in Guest Access](" + link + ")")
	}
	return b.String()
}

// escapeMarkdown makes guest text show as plain text in HA's markdown.
// HA renders notifications with marked (GitHub flavor) and lets a few HTML
// tags through, <a> among them. A backslash before an ASCII punctuation
// character makes it a literal in CommonMark, so every one gets one. That
// stops links ([x](y)), raw HTML (<a>), emphasis, code, and the bare URL,
// www. and email autolinks, which need an unbroken "://", "www." or "@x.".
// Clean drops line breaks and other control characters first, so no
// block syntax can start a line.
func escapeMarkdown(s string) string {
	s = Clean(s)
	var b strings.Builder
	b.Grow(len(s) + len(s)/4)
	for _, r := range s {
		if r < 0x80 && isASCIIPunct(byte(r)) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isASCIIPunct(c byte) bool {
	return (c >= '!' && c <= '/') || (c >= ':' && c <= '@') || (c >= '[' && c <= '`') || (c >= '{' && c <= '~')
}
